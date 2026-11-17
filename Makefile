.PHONY: test test-go test-android test-scripts test-web run compose-up compose-down

test: test-go test-scripts test-web test-android

test-go:
	go test -race -cover ./...
	go vet ./...

test-android:
	cd android && gradle testDebugUnitTest

test-scripts:
	bash -n scripts/android-debloat.sh
	bash -n scripts/test-android-debloat.sh
	bash scripts/test-android-debloat.sh

test-web:
	bash scripts/check-web.sh

run:
	go run ./cmd/server

compose-up:
	docker compose up --build

compose-down:
	docker compose down
