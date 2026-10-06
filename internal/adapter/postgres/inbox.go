package postgres

import (
	"context"

	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func (r *inboxRepository) Claim(ctx context.Context, m ports.InboxMessage) (bool, error) {
	if !r.transactional {
		return false, ports.ErrTransactionRequired
	}
	tag, err := r.db.Exec(ctx, `INSERT INTO inbox_messages(consumer_name,message_id,payload_hash,received_at) VALUES($1,$2,$3,$4) ON CONFLICT(consumer_name,message_id) DO NOTHING`, m.ConsumerName, m.MessageID, m.PayloadHash, m.ReceivedAt)
	return tag.RowsAffected() == 1, mapError(err)
}
func (r *inboxRepository) GetForUpdate(ctx context.Context, consumer, id string) (ports.InboxMessage, error) {
	if !r.transactional {
		return ports.InboxMessage{}, ports.ErrTransactionRequired
	}
	var m ports.InboxMessage
	err := r.db.QueryRow(ctx, `SELECT consumer_name,message_id,payload_hash,received_at,completed_at FROM inbox_messages WHERE consumer_name=$1 AND message_id=$2 FOR UPDATE`, consumer, id).Scan(&m.ConsumerName, &m.MessageID, &m.PayloadHash, &m.ReceivedAt, &m.CompletedAt)
	return m, mapError(err)
}
