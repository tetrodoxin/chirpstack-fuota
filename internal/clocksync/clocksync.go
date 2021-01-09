package clocksync

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/gofrs/uuid"

	"github.com/brocaar/chirpstack-api/go/v3/as/external/api"
	"github.com/brocaar/chirpstack-api/go/v3/as/integration"
	"github.com/brocaar/chirpstack-fuota-server/internal/client/as"
	"github.com/brocaar/chirpstack-fuota-server/internal/config"
	"github.com/brocaar/chirpstack-fuota-server/internal/eventhandler"
	"github.com/brocaar/chirpstack-fuota-server/internal/storage"

	"github.com/brocaar/lorawan"
	"github.com/brocaar/lorawan/applayer/clocksync"
	"github.com/brocaar/lorawan/applayer/fragmentation"
	"github.com/brocaar/lorawan/gps"
)

var id uuid.UUID

// Setup configures the Handler.
func Setup(c *config.Config) error {
	log.Info("clocksync: setup clocksync module")

	id := uuid.Must(uuid.FromString("48a11de7-60f0-4a28-8b55-d5ffe902c609"))

	eventhandler.Get().RegisterUplinkEventFunc(id, HandleUplinkEvent)

	return nil
}

// HandleUplinkEvent is the hook for clocksync messages
func HandleUplinkEvent(ctx context.Context, pl integration.UplinkEvent) error {

	if uint8(pl.FPort) == clocksync.DefaultFPort {
		var devEUI lorawan.EUI64
		copy(devEUI[:], pl.DevEui)

		if err := handleClocksyncSetupCommand(ctx, devEUI, pl.Data); err != nil {
			return fmt.Errorf("handle clocksync setup command error: %w", err)
		}
	}
	return nil
}

func handleClocksyncSetupCommand(ctx context.Context, devEUI lorawan.EUI64, b []byte) error {
	var cmd clocksync.Command
	if err := cmd.UnmarshalBinary(true, b); err != nil {
		return fmt.Errorf("unmarshal command error: %w", err)
	}

	log.WithFields(log.Fields{
		"dev_eui": devEUI,
		"cid":     cmd.CID,
	}).Info("clocksync: clocksync-setup command received")

	switch cmd.CID {
	case clocksync.AppTimeReq:
		pl, ok := cmd.Payload.(*clocksync.AppTimeReqPayload)
		if !ok {
			return fmt.Errorf("expected *clocksync.AppTimeReqPayload, got: %T", cmd.Payload)
		}
		return handleCsAppTimeReq(ctx, devEUI, pl)
	}

	return nil
}

func handleCsAppTimeReq(ctx context.Context, devEUI lorawan.EUI64, pl *clocksync.AppTimeReqPayload) error {
	log.WithFields(log.Fields{
		"DevEUI":      devEUI,
		"DeviceTime":  pl.DeviceTime,
		"AnsRequired": pl.Param.AnsRequired,
		"TokenReq":    pl.Param.TokenReq,
	}).Info("clocksync: AppTimeReqPayload received")

	gpsNow := uint32((gps.Time(time.Now()).TimeSinceGPSEpoch() / time.Second) & 0xffffffff)
	deviceDiff := (int32)(gpsNow - pl.DeviceTime)

	if !pl.Param.AnsRequired && deviceDiff < 5 && deviceDiff > -5 {
		log.WithFields(log.Fields{
			"DevEUI":   devEUI,
			"dev_diff": deviceDiff,
		}).Info("clocksync: device requested no answer and delta was low.")
		return nil
	}

	cmd := clocksync.Command{
		CID: clocksync.AppTimeAns,
		Payload: &clocksync.AppTimeAnsPayload{
			TimeCorrection: deviceDiff,
			Param: clocksync.AppTimeAnsPayloadParam{
				TokenAns: pl.Param.TokenReq,
			},
		},
	}

	b, err := cmd.MarshalBinary()
	if err != nil {
		return fmt.Errorf("marshal binary error: %w", err)
	}

	_, err = as.DeviceQueueClient().Enqueue(ctx, &api.EnqueueDeviceQueueItemRequest{
		DeviceQueueItem: &api.DeviceQueueItem{
			DevEui: devEUI.String(),
			FPort:  uint32(fragmentation.DefaultFPort),
			Data:   b,
		},
	})
	if err != nil {
		log.WithError(err).WithFields(log.Fields{
			"dev_eui": devEUI,
		}).Error("clocksync: enqueue payload error")
		return fmt.Errorf("enqueue payload error: %w", err)
	}

	now := time.Now()
	csd, err := storage.GetClocksyncDevice(ctx, storage.DB(), devEUI)
	switch err {
	case nil:
		csd.LastSyncAt = &now
		if err := storage.UpdateClocksyncDevice(ctx, storage.DB(), &csd); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"DevEUI":   devEUI,
				"dev_diff": deviceDiff,
			}).Warn("clocksync: could not update ClocksyncDevice db entry.")
		}
		break

	case sql.ErrNoRows:
		csd = storage.ClocksyncDevice{
			DevEUI:     devEUI,
			CreatedAt:  now,
			UpdatedAt:  now,
			LastSyncAt: &now,
		}

		if err := storage.CreateClocksyncDevice(ctx, storage.DB(), &csd); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"DevEUI":   devEUI,
				"dev_diff": deviceDiff,
			}).Warn("clocksync: could not create ClocksyncDevice db entry.")
		}
		break

	default:
		log.WithError(err).WithFields(log.Fields{
			"DevEUI":   devEUI,
			"dev_diff": deviceDiff,
		}).Warn("clocksync: could not read ClocksyncDevice from db.")
	}

	return nil
}
