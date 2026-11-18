package domain

import "testing"

func TestConfigPatchApplyHonorsExplicitFalseAndZeroValues(t *testing.T) {
	current := DefaultConfig()
	current.MinDistanceMeters = 25

	enabled := false
	minDistance := 0.0
	syncInterval := 300
	patched := ConfigPatch{
		Enabled:             &enabled,
		MinDistanceMeters:   &minDistance,
		SyncIntervalSeconds: &syncInterval,
	}.Apply(current)

	if patched.Enabled {
		t.Fatal("explicit enabled=false was ignored")
	}
	if patched.MinDistanceMeters != 0 {
		t.Fatalf("explicit min_distance_meters=0 was ignored: %v", patched.MinDistanceMeters)
	}
	if patched.SyncIntervalSeconds != 300 {
		t.Fatalf("sync interval = %d, want 300", patched.SyncIntervalSeconds)
	}
	if patched.LocationIntervalSeconds != current.LocationIntervalSeconds {
		t.Fatalf("unmodified field changed: %d", patched.LocationIntervalSeconds)
	}
}

func TestConfigPatchApplyIgnoresVersion(t *testing.T) {
	current := DefaultConfig()
	version := int64(99)
	patched := ConfigPatch{Version: &version}.Apply(current)
	if patched.Version != current.Version {
		t.Fatalf("patch version should be ignored: %d", patched.Version)
	}
}
