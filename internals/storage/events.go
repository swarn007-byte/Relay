package storage

import (
	"context"
	"database/sql"
)

func SaveEvent(
	ctx context.Context,
	db *sql.DB,
	eventID string,
	eventType string,
	targetURL string,
	payload []byte,
) error {

	//begin the transaction
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	//next is insert this into event table
	

	//next is insert into delivery job table

	return tx.Commit()
}
