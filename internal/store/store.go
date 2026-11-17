package store

import (
	"context"
	"errors"
	"time"

	"github.com/roman/kidstracker/internal/domain"
)

var (
	ErrNotFound          = errors.New("not found")
	ErrInvalidEnrollment = errors.New("invalid or already-used enrollment code")
)

type Repository interface {
	Ping(context.Context) error
	CreateDevice(context.Context, string, string, string, domain.Config) (domain.Device, error)
	Enroll(context.Context, string, string) (domain.Device, error)
	DeviceByToken(context.Context, string) (domain.Device, error)
	RecordHeartbeat(context.Context, string, *int, *bool) error
	AcknowledgeConfig(context.Context, string, int64) (bool, error)
	RecordTelemetry(context.Context, string, domain.Telemetry) error
	RecordLocations(context.Context, string, []domain.Location) error
	GetConfig(context.Context, string) (domain.Config, error)
	ListDevices(context.Context) ([]domain.Device, error)
	UpdateConfig(context.Context, string, domain.Config) (domain.Device, error)
	RevokeDevice(context.Context, string) error
	DeleteDevice(context.Context, string) error
	LocationHistory(context.Context, string, time.Time, time.Time, int) ([]domain.Location, error)
	TelemetryHistory(context.Context, string, time.Time, time.Time, int) ([]domain.Telemetry, error)
	GetMQTTSettings(context.Context) (domain.MQTTRecord, error)
	SaveMQTTSettings(context.Context, domain.MQTTSettings, []byte) error
}
