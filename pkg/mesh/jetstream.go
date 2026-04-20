package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"openbooking/pkg/protocol"
)

type JetStreamMesh struct {
	Connection *nats.Conn
	JetStream  nats.JetStreamContext
}

func Connect(url string, opts ...nats.Option) (*JetStreamMesh, error) {
	nc, err := nats.Connect(url, opts...)
	if err != nil {
		return nil, err
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, err
	}
	return &JetStreamMesh{Connection: nc, JetStream: js}, nil
}

func (m *JetStreamMesh) EnsureStream(name string, subjects ...string) error {
	if m.JetStream == nil {
		return fmt.Errorf("jetstream context is nil")
	}
	_, err := m.JetStream.AddStream(&nats.StreamConfig{
		Name:      name,
		Subjects:  subjects,
		Retention: nats.LimitsPolicy,
		Storage:   nats.FileStorage,
		Replicas:  1,
	})
	if err == nil || err == nats.ErrStreamNameAlreadyInUse {
		return nil
	}
	if _, lookupErr := m.JetStream.StreamInfo(name); lookupErr == nil {
		return nil
	}
	return err
}

func (m *JetStreamMesh) PublishCommit(commit protocol.Commit) (*nats.PubAck, error) {
	payload, err := json.Marshal(commit)
	if err != nil {
		return nil, err
	}
	return m.JetStream.Publish(commit.Subject(), payload)
}

func (m *JetStreamMesh) PublishIntent(providerDID string, payload any) (*nats.PubAck, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return m.JetStream.Publish(protocol.IntentSubject(providerDID), body)
}

func (m *JetStreamMesh) ConsumeCommits(ctx context.Context, durable, actorDID string, handler func(protocol.Commit) error) error {
	sub, err := m.JetStream.PullSubscribe(protocol.RepoCommitSubject(actorDID), durable)
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		msgs, err := sub.Fetch(1, nats.MaxWait(250*time.Millisecond))
		if err != nil && err != nats.ErrTimeout {
			return err
		}
		for _, msg := range msgs {
			var commit protocol.Commit
			if err := json.Unmarshal(msg.Data, &commit); err != nil {
				msg.Nak()
				return err
			}
			if err := handler(commit); err != nil {
				msg.Nak()
				return err
			}
			msg.Ack()
		}
	}
}

func (m *JetStreamMesh) Close() {
	if m.Connection != nil {
		m.Connection.Close()
	}
}
