package clocksync

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/gofrs/uuid"
	"github.com/golang/protobuf/ptypes"

	"github.com/brocaar/chirpstack-api/go/v3/as/external/api"
	"github.com/brocaar/chirpstack-api/go/v3/as/integration"
	"github.com/brocaar/chirpstack-fuota-server/internal/client/as"
	"github.com/brocaar/chirpstack-fuota-server/internal/config"
	"github.com/brocaar/chirpstack-fuota-server/internal/eventhandler"
	"github.com/brocaar/chirpstack-fuota-server/internal/storage"

	"github.com/brocaar/lorawan"
	"github.com/brocaar/lorawan/applayer/clocksync"
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

		if len(pl.Data) == 0 {
			log.WithFields(log.Fields{
				"dev_eui": devEUI,
			}).Info("clocksync: received empty frame on port %d. Nothing done.", uint8(pl.FPort))

			return nil
		}

		if err := handleClocksyncSetupCommand(ctx, devEUI, pl.Data); err != nil {
			return fmt.Errorf("handle clocksync setup command error: %w", err)
		}
	}

	return nil
}

func handleClocksyncSetupCommand(ctx context.Context, devEUI lorawan.EUI64, ev integration.UplinkEvent) error {
	var cmd clocksync.Command
	if err := cmd.UnmarshalBinary(true, ev.Data); err != nil {
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

		gpsTimeOfRx := getRxGpsTimeOfUplinkEvent(ev)

		return handleCsAppTimeReq(ctx, devEUI, gpsTimeOfRx, pl)
	}

	return nil
}

func handleCsAppTimeReq(ctx context.Context, devEUI lorawan.EUI64, gpsTimeOfRx time.Duration, pl *clocksync.AppTimeReqPayload) error {
	devTimeString := time.Time(gps.NewTimeFromTimeSinceGPSEpoch(time.Duration(pl.DeviceTime) * time.Second)).Format(time.RFC3339)

	log.WithFields(log.Fields{
		"DevEUI":        devEUI,
		"DeviceTime":    pl.DeviceTime,
		"DeviceTimeStr": devTimeString,
		"AnsRequired":   pl.Param.AnsRequired,
		"TokenReq":      pl.Param.TokenReq,
	}).Info("clocksync: AppTimeReqPayload received")

	networkGpsTime := uint32((gpsTimeOfRx / time.Second) & 0xffffffff)
	deviceDiff := (int32)(networkGpsTime - pl.DeviceTime)

	if !pl.Param.AnsRequired && deviceDiff < 5 && deviceDiff > -5 {
		log.WithFields(log.Fields{
			"DevEUI":   devEUI,
			"dev_diff": deviceDiff,
		}).Info("clocksync: device requested no answer and delta was low.")
		return nil
	}

	ansCmd := clocksync.Command{
		CID: clocksync.AppTimeAns,
		Payload: &clocksync.AppTimeAnsPayload{
			TimeCorrection: deviceDiff,
			Param: clocksync.AppTimeAnsPayloadParam{
				TokenAns: pl.Param.TokenReq,
			},
		},
	}

	b, err := ansCmd.MarshalBinary()
	if err != nil {
		return fmt.Errorf("marshal binary error: %w", err)
	}

	_, err = as.DeviceQueueClient().Enqueue(ctx, &api.EnqueueDeviceQueueItemRequest{
		DeviceQueueItem: &api.DeviceQueueItem{
			DevEui: devEUI.String(),
			FPort:  uint32(clocksync.DefaultFPort),
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

	log.WithFields(log.Fields{
		"DevEUI":         devEUI,
		"TimeCorrection": deviceDiff,
	}).Info("clocksync: AppTimeAns sent.")

	return nil
}

func getRxGpsTimeOfUplinkEvent(pl integration.UplinkEvent) time.Duration {
	// get uplink time
	var gpsTime time.Duration
	var timeField time.Time
	var err error

	for _, rxInfo := range pl.RxInfo {
		if rxInfo.TimeSinceGpsEpoch != nil {
			gpsTime, err = ptypes.Duration(rxInfo.TimeSinceGpsEpoch)
			if err != nil {
				log.WithError(err).Error("clocksync: time since gps epoch to duration error")
				continue
			}
		} else if rxInfo.Time != nil {
			timeField, err = ptypes.Timestamp(rxInfo.Time)
			if err != nil {
				log.WithError(err).Error("clocksync: time to timestamp error")
				continue
			}
		}
	}

	// fallback on time field when time since GPS epoch is not available
	if gpsTime == 0 {
		// fallback on current server time when time field is not available
		if timeField.IsZero() {
			timeField = time.Now()
		}
		gpsTime = gps.Time(timeField).TimeSinceGPSEpoch()
	}

	return gpsTime
}
