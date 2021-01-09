package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"
	log "github.com/sirupsen/logrus"

	"github.com/brocaar/lorawan"
)

// ClocksyncDevice represents a single device in the clocksync workflow
type ClocksyncDevice struct {
	DevEUI     lorawan.EUI64 `db:"dev_eui"`
	CreatedAt  time.Time     `db:"created_at"`
	UpdatedAt  time.Time     `db:"updated_at"`
	LastSyncAt *time.Time    `db:"last_sync_at"`
}

// CreateClocksyncDevice creates the given ClocksyncDevice.
func CreateClocksyncDevice(ctx context.Context, db sqlx.Execer, cd *ClocksyncDevice) error {
	now := time.Now().Round(time.Millisecond)
	cd.CreatedAt = now
	cd.UpdatedAt = now

	_, err := db.Exec(`
		insert into clocksync_device (
			dev_eui,
			created_at,
			updated_at,
			last_sync_at
		) values ($1, $2, $3, $4)`,
		cd.DevEUI,
		cd.CreatedAt,
		cd.UpdatedAt,
		cd.LastSyncAt,
	)
	if err != nil {
		return fmt.Errorf("sql exec error: %w", err)
	}

	log.WithFields(log.Fields{
		"dev_eui": cd.DevEUI,
	}).Info("storage: clocksync device created")

	return nil
}

// GetClocksyncDevice returns the ClocksyncDevice for the given DevEUI.
// If no device for provided DevEUI is found, 'sql.ErrNoRows' is returned for 'error'
func GetClocksyncDevice(ctx context.Context, db sqlx.Queryer, devEUI lorawan.EUI64) (ClocksyncDevice, error) {
	var cd ClocksyncDevice
	err := sqlx.Get(db, &cd, "select * from clocksync_device where dev_eui = $1", devEUI)

	switch err {
	case nil:
		return cd, nil
	case sql.ErrNoRows:
		return cd, err
	default:
		return cd, fmt.Errorf("sql select error: %w", err)
	}
}

// UpdateClocksyncDevice updates the given ClocksyncDevice.
func UpdateClocksyncDevice(ctx context.Context, db sqlx.Execer, cd *ClocksyncDevice) error {
	cd.UpdatedAt = time.Now()

	res, err := db.Exec(`
		update clocksync_device set
			updated_at = $2,
			last_sync_at = $3
		where
			dev_eui = $1`,
		cd.DevEUI,
		cd.UpdatedAt,
		cd.LastSyncAt,
	)
	if err != nil {
		return fmt.Errorf("sql update error: %w", err)
	}
	ra, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("get rows affected error: %w", err)
	}
	if ra == 0 {
		return ErrDoesNotExist
	}

	log.WithFields(log.Fields{
		"dev_eui": cd.DevEUI,
	}).Info("storage: clocksync device updated")

	return nil
}
