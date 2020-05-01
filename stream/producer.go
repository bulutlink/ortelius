// (c) 2020, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package stream

import (
	"context"
	"encoding/binary"
	"path"

	"github.com/ava-labs/gecko/ids"
	"github.com/ava-labs/gecko/utils/hashing"
	"github.com/segmentio/kafka-go"
	"nanomsg.org/go/mangos/v2"
	"nanomsg.org/go/mangos/v2/protocol"
	"nanomsg.org/go/mangos/v2/protocol/sub"

	"github.com/ava-labs/ortelius/cfg"
	"github.com/ava-labs/ortelius/stream/record"
)

// producer reads from the socket and writes to the event stream
type producer struct {
	chainID     ids.ID
	sock        protocol.Socket
	binFilterFn binFilterFn
	writer      *kafka.Writer
}

// NewProducer creates a producer using the given config
func NewProducer(conf *cfg.ClientConfig, _ uint32, chainID ids.ID) (Processor, error) {
	p := &producer{
		chainID:     chainID,
		binFilterFn: newBinFilterFn(conf.FilterConfig.Min, conf.FilterConfig.Max),
	}

	var err error
	p.sock, err = createIPCSocket("ipc://" + path.Join(conf.IPCRoot, chainID.String()) + ".ipc")
	if err != nil {
		return nil, err
	}

	p.writer = kafka.NewWriter(kafka.WriterConfig{
		Brokers:  conf.KafkaConfig.Brokers,
		Topic:    chainID.String(),
		Balancer: &kafka.LeastBytes{},
	})

	return p, nil
}

// Close shuts down the producer
func (p *producer) Close() error {
	return p.writer.Close()
}

// ProcessNextMessage takes in a Message from the IPC socket and writes it to
// Kafka
func (p *producer) ProcessNextMessage() (*Message, error) {
	// Get bytes from IPC
	rawMsg, err := p.sock.Recv()
	if err != nil {
		return nil, err
	}

	// If we match the filter then stop now
	if p.binFilterFn(rawMsg) {
		return nil, nil
	}

	// Create a Message object
	msg := &Message{
		id:      ids.NewID(hashing.ComputeHash256Array(rawMsg)),
		chainID: p.chainID,
		body:    record.Marshal(rawMsg),
	}

	// Send Message to Kafka
	ctx, cancelFn := context.WithTimeout(context.Background(), defaultKafkaReadTimeout)
	defer cancelFn()
	err = p.writer.WriteMessages(ctx, kafka.Message{
		Value: msg.body,
		Key:   msg.id.Bytes(),
	})
	if err != nil {
		return nil, err
	}

	return msg, err
}

// createIPCSocket creates a new socket connection to the configured IPC URL
func createIPCSocket(url string) (protocol.Socket, error) {
	// Create and open a connection to the IPC socket
	sock, err := sub.NewSocket()
	if err != nil {
		return nil, err
	}

	if err = sock.Dial(url); err != nil {
		return nil, err
	}

	// Subscribe to all topics
	if err = sock.SetOption(mangos.OptionSubscribe, []byte("")); err != nil {
		return nil, err
	}

	return sock, nil
}

type binFilterFn func([]byte) bool

// newBinFilterFn returns a binFilterFn with the given range
func newBinFilterFn(min uint32, max uint32) binFilterFn {
	return func(input []byte) bool {
		value := binary.LittleEndian.Uint32(input[:4])
		return !(value < min || value > max)
	}
}