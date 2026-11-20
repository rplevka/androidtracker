package store

import (
	"context"
	"errors"
	"time"

	"github.com/roman/kidstracker/internal/domain"
)

var (
	ErrNotFound            = errors.New("not found")
	ErrDuplicate           = errors.New("already exists")
	ErrInvalidEnrollment   = errors.New("invalid or already-used enrollment code")
	ErrInvalidZoneGeometry = errors.New("invalid zone geometry")
)

type Repository interface {
	Ping(context.Context) error
	CreateDevice(context.Context, string, string, string, domain.Config) (domain.Device, error)
	Enroll(context.Context, string, string) (domain.Device, error)
	DeviceByToken(context.Context, string) (domain.Device, error)
	RecordHeartbeat(context.Context, string, *int, *bool) error
	AcknowledgeConfig(context.Context, string, int64) (bool, error)
	RecordTelemetry(context.Context, string, domain.Telemetry) error
	RecordLocations(context.Context, string, []domain.Location) (*domain.ZoneState, error)
	GetConfig(context.Context, string) (domain.Config, error)
	ListZones(context.Context) ([]domain.Zone, error)
	GetZone(context.Context, string) (domain.Zone, error)
	CreateZone(context.Context, string, domain.ZoneInput) (domain.Zone, error)
	UpdateZone(context.Context, string, domain.ZonePatch) (domain.Zone, []string, error)
	DeleteZone(context.Context, string) ([]string, error)
	ListDeviceZones(context.Context, string, bool) ([]domain.Zone, error)
	ZoneEventsHistory(context.Context, string, time.Time, time.Time, int) ([]domain.ZoneEvent, error)
	AcknowledgeZones(context.Context, string, string) error
	ListDevices(context.Context) ([]domain.Device, error)
	UpdateConfig(context.Context, string, domain.Config) (domain.Device, error)
	RevokeDevice(context.Context, string) error
	DeleteDevice(context.Context, string) error
	LocationHistory(context.Context, string, time.Time, time.Time, int) ([]domain.Location, error)
	TelemetryHistory(context.Context, string, time.Time, time.Time, int) ([]domain.Telemetry, error)
	GetMQTTSettings(context.Context) (domain.MQTTRecord, error)
	SaveMQTTSettings(context.Context, domain.MQTTSettings, []byte) error
	CreateUser(context.Context, string, string, string, string, string) (domain.User, error)
	UserByUsername(context.Context, string) (domain.User, error)
	ListUsers(context.Context) ([]domain.User, error)
	MarkUserLogin(context.Context, string) error
	CreateSession(context.Context, []byte, *string, string, string, time.Time) error
	SessionByToken(context.Context, []byte) (domain.Session, error)
	DeleteSession(context.Context, []byte) error
}
