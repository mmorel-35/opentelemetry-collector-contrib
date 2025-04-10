// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package azureblobreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/azureblobreceiver"

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/messaging/azeventhubs"
	"go.uber.org/zap"
)

type blobEventHandler interface {
	run(ctx context.Context) error
	close(ctx context.Context) error
	setLogsDataConsumer(logsDataConsumer logsDataConsumer)
	setTracesDataConsumer(tracesDataConsumer tracesDataConsumer)
}

type azureBlobEventHandler struct {
	blobClient               blobClient
	logsDataConsumer         logsDataConsumer
	tracesDataConsumer       tracesDataConsumer
	logsContainerName        string
	tracesContainerName      string
	eventHubConnectionString string
	consumerClient           *azeventhubs.ConsumerClient
	logger                   *zap.Logger
}

var _ blobEventHandler = (*azureBlobEventHandler)(nil)

const (
	blobCreatedEventType = "Microsoft.Storage.BlobCreated"
)

func (p *azureBlobEventHandler) run(ctx context.Context) error {
	if p.consumerClient != nil {
		return nil
	}

	consumerClient, err := azeventhubs.NewConsumerClientFromConnectionString(p.eventHubConnectionString, "", azeventhubs.DefaultConsumerGroup, nil)
	if err != nil {
		return err
	}

	p.consumerClient = consumerClient

	runtimeInfo, err := p.consumerClient.GetEventHubProperties(ctx, nil)
	if err != nil {
		return err
	}

	for _, partitionID := range runtimeInfo.PartitionIDs {
		partitionClient, err := p.consumerClient.NewPartitionClient(partitionID, nil)
		if err != nil {
			return err
		}
		defer partitionClient.Close(context.Background())
		for {
			events, err := partitionClient.ReceiveEvents(ctx, 100, nil)
			if err != nil {
				return err
			}
			for _, event := range events {
				err = p.newMessageHandler(ctx, event)
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (p *azureBlobEventHandler) newMessageHandler(ctx context.Context, event *azeventhubs.ReceivedEventData) error {
	type eventData struct {
		Topic           string
		Subject         string
		EventType       string
		ID              string
		Data            map[string]any
		DataVersion     string
		MetadataVersion string
		EventTime       string
	}
	var eventDataSlice []eventData
	marshalErr := json.Unmarshal(event.Body, &eventDataSlice)
	if marshalErr != nil {
		return marshalErr
	}
	subject := eventDataSlice[0].Subject
	containerName := strings.Split(strings.Split(subject, "containers/")[1], "/")[0]
	eventType := eventDataSlice[0].EventType
	blobName := strings.Split(subject, "blobs/")[1]

	if eventType == blobCreatedEventType {
		blobData, err := p.blobClient.readBlob(ctx, containerName, blobName)
		if err != nil {
			return err
		}
		switch containerName {
		case p.logsContainerName:
			err = p.logsDataConsumer.consumeLogsJSON(ctx, blobData.Bytes())
			if err != nil {
				return err
			}
		case p.tracesContainerName:
			err = p.tracesDataConsumer.consumeTracesJSON(ctx, blobData.Bytes())
			if err != nil {
				return err
			}
		default:
			p.logger.Debug("Unknown container name", zap.String("containerName", containerName))
		}
	}

	return nil
}

func (p *azureBlobEventHandler) close(ctx context.Context) error {
	if p.consumerClient != nil {
		err := p.consumerClient.Close(ctx)
		if err != nil {
			return err
		}
		p.consumerClient = nil
	}
	return nil
}

func (p *azureBlobEventHandler) setLogsDataConsumer(logsDataConsumer logsDataConsumer) {
	p.logsDataConsumer = logsDataConsumer
}

func (p *azureBlobEventHandler) setTracesDataConsumer(tracesDataConsumer tracesDataConsumer) {
	p.tracesDataConsumer = tracesDataConsumer
}

func newBlobEventHandler(eventHubConnectionString string, logsContainerName string, tracesContainerName string, blobClient blobClient, logger *zap.Logger) *azureBlobEventHandler {
	return &azureBlobEventHandler{
		blobClient:               blobClient,
		logsContainerName:        logsContainerName,
		tracesContainerName:      tracesContainerName,
		eventHubConnectionString: eventHubConnectionString,
		logger:                   logger,
	}
}
