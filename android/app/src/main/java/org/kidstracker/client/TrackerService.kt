package org.kidstracker.client

import android.Manifest
import android.content.IntentFilter
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.location.Location
import android.location.LocationListener
import android.location.LocationManager
import android.os.Build
import android.os.BatteryManager
import android.os.Bundle
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.os.PowerManager
import android.os.SystemClock
import org.json.JSONArray
import org.json.JSONObject
import java.net.HttpURLConnection
import java.net.URL
import java.time.Instant
import java.util.UUID
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.Executors

private data class ConfigFetchResult(val config: TrackerConfig, val zonesEtag: String?)

class TrackerService : Service() {
    private val executor = Executors.newSingleThreadExecutor()
    private val mainHandler = Handler(Looper.getMainLooper())
    private val preferences by lazy { getSharedPreferences(PREFERENCES, Context.MODE_PRIVATE) }
    private var locationManager: LocationManager? = null
    private val locationListeners = CopyOnWriteArrayList<LocationListener>()
    private var wakeLock: PowerManager.WakeLock? = null
    private val lifecycleLock = Any()
    private var cycleRunning = false
    private var latestStartId = 0
    @Volatile private var cycleFinished = false
    @Volatile private var zoneTransitionDetected = false
    private var scheduledWakeAt = 0L
    private var cycleStartedAt = 0L

    override fun onCreate() {
        super.onCreate()
        createNotificationChannel()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForeground(NOTIFICATION_ID, notification())
        val manualSync = intent?.action == ACTION_SYNC_NOW
        synchronized(lifecycleLock) {
            latestStartId = startId
            if (cycleRunning) return START_NOT_STICKY
            cycleRunning = true
            cycleFinished = false
            cycleStartedAt = SystemClock.elapsedRealtime()
            scheduledWakeAt = if (manualSync) 0L else preferences.getLong(PREF_NEXT_WAKE, 0L)
        }
        // A previous cycle may have stopped the foreground notification after
        // this start was delivered but before it acquired the lifecycle lock.
        startForeground(NOTIFICATION_ID, notification())
        acquireWakeLock()
        // If the process dies before the cycle finishes, this alarm restarts it.
        TrackerScheduler.schedule(this, WATCHDOG_DELAY_SECONDS)
        executor.execute { runCycle(manualSync) }
        return START_NOT_STICKY
    }

    private fun runCycle(manualSync: Boolean) {
        try {
            if (manualSync) syncNow() else beginCycle()
        } catch (t: Throwable) {
            markSyncFailure(if (t is Exception) t else Exception(t))
            finishCycle(nextWakeDelaySeconds(cachedConfig(), batteryPercent(), isCharging()))
        }
    }

    private fun beginCycle() {
        zoneTransitionDetected = false
        val base = preferences.getString(PREF_SERVER, null)
        val token = preferences.getString(PREF_TOKEN, null)
        if (base.isNullOrBlank() || token.isNullOrBlank()) return stopPermanently()
        val previousConfig = cachedConfig()
        var config = previousConfig
        val now = System.currentTimeMillis()
        val battery = batteryPercent()
        val charging = isCharging()
        saveBatteryState(battery, charging)
        val syncInterval = CyclePolicy.effectiveIntervalSeconds(
            if (previousConfig.enabled) previousConfig.syncIntervalSeconds else previousConfig.sleepDurationSeconds,
            battery,
            charging,
        )
        val syncDue = isDue(PREF_LAST_SYNC_ATTEMPT_AT, syncInterval, now)
        if (syncDue) preferences.edit().putLong(PREF_LAST_SYNC_ATTEMPT_AT, now).apply()

        var configFetched = false
        var configAcknowledged = false
        var configError: Exception? = null
        if (syncDue) {
            preferences.edit().putString(PREF_SYNC_STATUS, "Syncing").remove(PREF_SYNC_ERROR).apply()
            try {
                val fetched = fetchConfig(base, token)
                config = fetched.config
                saveConfig(config)
                configFetched = true
                val zonesEtag = refreshZones(base, token, fetched.zonesEtag)
                configAcknowledged = acknowledgeConfig(
                    base,
                    token,
                    config.version,
                    zonesEtag,
                    DeviceTelemetry.syncJson(
                        this,
                        config.version,
                        syncDurationMS(),
                        queueSize(),
                        wakeDelaySeconds(),
                    ),
                )
            } catch (_: DeviceAuthorizationException) {
                return markRevoked()
            } catch (error: Exception) {
                configError = error
                markSyncFailure(error)
            }
        }

        val locationDue = config.enabled && (
            (!previousConfig.enabled && config.enabled) ||
                isDue(PREF_LAST_LOCATION_ATTEMPT_AT,
                    CyclePolicy.effectiveIntervalSeconds(config.locationIntervalSeconds, battery, charging), now)
            )
        if (locationDue) preferences.edit().putLong(PREF_LAST_LOCATION_ATTEMPT_AT, now).apply()
        val lowBattery = !charging && battery in 0..5
        if (locationDue && !lowBattery && hasLocationPermission()) {
            mainHandler.post {
                requestLocation(base, token, config, syncDue, configFetched, configAcknowledged, configError)
            }
            return
        }

        var uploadError: Exception? = null
        var uploadSucceeded = queueSize() == 0
        if (syncDue && !lowBattery) {
            try {
                uploadQueued(base, token)
                uploadSucceeded = true
            } catch (_: DeviceAuthorizationException) {
                return markRevoked()
            } catch (error: Exception) {
                uploadError = error
            }
        }
        if (syncDue) {
            finishSync(configFetched, configAcknowledged, configError, uploadError,
                uploadSucceeded, lowBattery && queueSize() > 0)
        }
        finishCycle(nextWakeDelaySeconds(config, battery, charging))
    }

    private fun requestLocation(
        base: String,
        token: String,
        config: TrackerConfig,
        syncDue: Boolean,
        configFetched: Boolean,
        configAcknowledged: Boolean,
        configError: Exception?,
    ) {
        val locationStartedAt = SystemClock.elapsedRealtime()
        try {
            val manager = getSystemService(Context.LOCATION_SERVICE) as LocationManager
            locationManager = manager
            // Request both providers. GPS wins when it reports; otherwise the best
            // network fix (cell/Wi-Fi) is used after the GPS timeout. The network
            // provider is attempted even when the platform reports it disabled:
            // some ROMs expose it as a Play Services overlay provider.
            val gpsEnabled = isProviderEnabled(manager, LocationManager.GPS_PROVIDER)

            val bestFixes = mutableMapOf<String, Location>()
            var completed = false
            var gpsPending = false

            fun complete(location: Location?) {
                if (completed || cycleFinished) return
                completed = true
                clearLocationListener()
                executor.execute {
                    if (cycleFinished) return@execute
                    if (location == null) {
                        finishLocationCycle(base, token, config, syncDue, configFetched,
                            configAcknowledged, configError)
                        return@execute
                    }
                    try {
                        appendLocation(
                            location,
                            config,
                            (SystemClock.elapsedRealtime() - locationStartedAt).coerceAtLeast(0L),
                        )
                    } catch (error: Exception) {
                        markSyncFailure(error)
                        finishCycle(nextWakeDelaySeconds(config, batteryPercent(), isCharging()))
                        return@execute
                    }
                    finishLocationCycle(base, token, config, syncDue, configFetched,
                        configAcknowledged, configError)
                }
            }

            fun listener(provider: String) = object : LocationListener {
                override fun onLocationChanged(location: Location) {
                    bestFixes[provider] = location
                    if (provider == LocationManager.GPS_PROVIDER || !gpsPending) {
                        complete(location)
                    }
                }
                @Deprecated("Deprecated by Android; retained for API 28 support")
                override fun onStatusChanged(provider: String?, status: Int, extras: Bundle?) = Unit
                override fun onProviderEnabled(provider: String) = Unit
                override fun onProviderDisabled(provider: String) = Unit
            }

            fun request(provider: String): Boolean {
                val updateListener = listener(provider)
                locationListeners.add(updateListener)
                return try {
                    manager.requestSingleUpdate(provider, updateListener, Looper.getMainLooper())
                    true
                } catch (_: SecurityException) {
                    locationListeners.remove(updateListener)
                    false
                } catch (_: Exception) {
                    locationListeners.remove(updateListener)
                    false
                }
            }

            if (gpsEnabled) gpsPending = request(LocationManager.GPS_PROVIDER)
            request(LocationManager.NETWORK_PROVIDER)
            request(FUSED_PROVIDER)
            if (locationListeners.isEmpty()) {
                complete(null)
                return
            }

            mainHandler.postDelayed({
                if (!completed) {
                    complete(
                        bestFixes[LocationManager.GPS_PROVIDER]
                            ?: bestFixes[LocationManager.NETWORK_PROVIDER]
                            ?: bestFixes[FUSED_PROVIDER],
                    )
                }
            }, config.gpsTimeoutSeconds.coerceIn(5, 180) * 1000L)
        } catch (_: SecurityException) {
            executor.execute {
                finishLocationCycle(base, token, config, syncDue, configFetched,
                    configAcknowledged, configError)
            }
        } catch (_: Exception) {
            executor.execute {
                finishLocationCycle(base, token, config, syncDue, configFetched,
                    configAcknowledged, configError)
            }
        }
    }

    private fun isProviderEnabled(manager: LocationManager, provider: String): Boolean = try {
        manager.isProviderEnabled(provider)
    } catch (_: Exception) {
        false
    }

    private fun hasLocationPermission(): Boolean =
        checkSelfPermission(Manifest.permission.ACCESS_FINE_LOCATION) == PackageManager.PERMISSION_GRANTED ||
            checkSelfPermission(Manifest.permission.ACCESS_COARSE_LOCATION) == PackageManager.PERMISSION_GRANTED

    private fun finishLocationCycle(
        base: String,
        token: String,
        config: TrackerConfig,
        syncDue: Boolean,
        configFetched: Boolean,
        configAcknowledged: Boolean,
        configError: Exception?,
    ) {
        val battery = batteryPercent()
        val charging = isCharging()
        saveBatteryState(battery, charging)
        val lowBattery = !charging && battery in 0..5
        var uploadError: Exception? = null
        var uploadSucceeded = queueSize() == 0
        val shouldUpload = syncDue || zoneTransitionDetected
        if (shouldUpload && !lowBattery) {
            try {
                uploadQueued(base, token)
                uploadSucceeded = true
            } catch (_: DeviceAuthorizationException) {
                markRevoked()
                return
            } catch (error: Exception) {
                uploadError = error
            }
        }
        if (syncDue) {
            finishSync(configFetched, configAcknowledged, configError, uploadError,
                uploadSucceeded, lowBattery && queueSize() > 0)
        } else if (zoneTransitionDetected) {
            when {
                uploadError != null -> markSyncFailure(uploadError)
                uploadSucceeded -> markSyncDeferred("Zone change uploaded immediately")
                else -> markSyncDeferred("Zone change queued; waiting for the next sync")
            }
        } else if (queueSize() > 0) {
            preferences.edit().putString(PREF_SYNC_STATUS, "Location queued for next sync").apply()
        }
        finishCycle(nextWakeDelaySeconds(config, battery, charging))
    }

    private fun finishSync(
        configFetched: Boolean,
        configAcknowledged: Boolean,
        configError: Exception?,
        uploadError: Exception?,
        uploadSucceeded: Boolean,
        holdingQueueForBattery: Boolean,
    ) {
        when {
            configError != null -> markSyncFailure(configError)
            uploadError != null -> markSyncFailure(uploadError)
            holdingQueueForBattery -> markSyncDeferred("Config synced; saved locations held at low battery")
            configFetched && configAcknowledged && uploadSucceeded -> markSyncSuccess()
            configFetched -> markSyncDeferred("Config changed on server; waiting for the next sync")
            else -> markSyncFailure(IllegalStateException("Sync did not complete"))
        }
    }

    private fun isDue(key: String, intervalSeconds: Int, now: Long): Boolean {
        val lastAttempt = preferences.getLong(key, 0L)
        return lastAttempt <= 0L || now - lastAttempt >= intervalSeconds.coerceAtLeast(1) * 1000L
    }

    private fun nextWakeDelaySeconds(config: TrackerConfig, battery: Int, charging: Boolean): Int {
        val now = System.currentTimeMillis()
        val syncInterval = CyclePolicy.effectiveIntervalSeconds(
            if (config.enabled) config.syncIntervalSeconds else config.sleepDurationSeconds,
            battery,
            charging,
        )
        val nextSync = nextDueTime(PREF_LAST_SYNC_ATTEMPT_AT, syncInterval, now)
        val nextTask = if (config.enabled) {
            val locationInterval = CyclePolicy.effectiveIntervalSeconds(config.locationIntervalSeconds, battery, charging)
            minOf(nextSync, nextDueTime(PREF_LAST_LOCATION_ATTEMPT_AT, locationInterval, now))
        } else {
            nextSync
        }
        val remainingMillis = (nextTask - now).coerceAtLeast(0L)
        return ((remainingMillis + 999L) / 1000L).coerceAtMost(604800L).toInt()
    }

    private fun nextDueTime(key: String, intervalSeconds: Int, now: Long): Long {
        val lastAttempt = preferences.getLong(key, 0L)
        return if (lastAttempt <= 0L) now else lastAttempt + intervalSeconds.coerceAtLeast(1) * 1000L
    }

    private fun appendLocation(location: Location, config: TrackerConfig, fixDurationMS: Long) {
        val zoneChanged = ZoneStore.evaluate(this, location)
        if (zoneChanged) zoneTransitionDetected = true
        val previousLat = preferences.getFloat(PREF_LAST_LAT, Float.NaN)
        val previousLon = preferences.getFloat(PREF_LAST_LON, Float.NaN)
        if (!previousLat.isNaN() && !previousLon.isNaN()) {
            val previous = Location("last").apply { latitude = previousLat.toDouble(); longitude = previousLon.toDouble() }
            if (!CyclePolicy.shouldRecord(location.distanceTo(previous), config.minDistanceMeters) && !zoneChanged) return
        }
        val source = LocationSource.fromProvider(location.provider)
        val item = JSONObject()
            .put("id", UUID.randomUUID().toString())
            .put("latitude", location.latitude)
            .put("longitude", location.longitude)
            .put("accuracy_meters", location.accuracy.toDouble())
            .put("location_source", source)
            .put("recorded_at", Instant.ofEpochMilli(location.time).toString())
        if (location.hasAltitude()) item.put("altitude_meters", location.altitude)
        if (location.hasSpeed()) item.put("speed_meters_per_second", location.speed)
        if (location.hasBearing()) item.put("bearing_degrees", location.bearing)
        val battery = batteryPercent()
        if (battery in 0..100) item.put("battery_percent", battery)
        item.put("charging", isCharging())
        DeviceTelemetry.addLocationFields(this, item)
        item.put("gps_fix_duration_ms", fixDurationMS.coerceIn(0L, 600000L))
        val queue = JSONArray(preferences.getString(PREF_QUEUE, "[]"))
        queue.put(item)
        val bounded = JSONArray()
        for (index in (queue.length() - MAX_QUEUED_LOCATIONS).coerceAtLeast(0) until queue.length()) bounded.put(queue.getJSONObject(index))
        preferences.edit().putString(PREF_QUEUE, bounded.toString())
            .putFloat(PREF_LAST_LAT, location.latitude.toFloat())
            .putFloat(PREF_LAST_LON, location.longitude.toFloat())
            .putLong(PREF_LAST_LOCATION_AT, location.time)
            .putString(PREF_LAST_LOCATION_SOURCE, source)
            .apply()
    }

    private fun uploadQueued(base: String, token: String) {
        val queue = JSONArray(preferences.getString(PREF_QUEUE, "[]"))
        if (queue.length() == 0) return
        val connection = request("$base/v1/locations/batch", token, "POST")
        connection.doOutput = true
        connection.setRequestProperty("Content-Type", "application/json")
        connection.setRequestProperty("Content-Encoding", "gzip")
        val payload = JSONObject().put("locations", queue).toString().toByteArray(Charsets.UTF_8)
        val compressedPayload = LocationBatchEncoder.gzip(payload)
        connection.setFixedLengthStreamingMode(compressedPayload.size)
        connection.outputStream.use { it.write(compressedPayload) }
        val status = connection.responseCode
        readResponse(connection, status)
        connection.disconnect()
        if (status == 401 || status == 403) throw DeviceAuthorizationException()
        if (status !in 200..299) throw IllegalStateException("Location upload failed: $status")
        preferences.edit().putString(PREF_QUEUE, "[]").apply()
    }

    private fun fetchConfig(base: String, token: String): ConfigFetchResult {
        val connection = request("$base/v1/config", token, "GET")
        val currentBattery = batteryPercent()
        if (currentBattery in 0..100) connection.setRequestProperty("X-Battery-Percent", currentBattery.toString())
        connection.setRequestProperty("X-Charging", isCharging().toString())
        try {
            val status = connection.responseCode
            val body = readResponse(connection, status)
            if (status == 401 || status == 403) throw DeviceAuthorizationException()
            if (status !in 200..299) throw IllegalStateException("Config request failed: $status")
            return ConfigFetchResult(
                parseConfig(JSONObject(body)),
                connection.getHeaderField("X-Zones-ETag"),
            )
        } finally {
            connection.disconnect()
        }
    }

    private fun refreshZones(base: String, token: String, serverEtag: String?): String? {
        val cachedEtag = ZoneStore.cachedEtag(this)
        if (serverEtag.isNullOrBlank()) return cachedEtag
        if (serverEtag == cachedEtag) return cachedEtag
        val connection = request("$base/v1/zones", token, "GET")
        if (!cachedEtag.isNullOrBlank()) connection.setRequestProperty("If-None-Match", cachedEtag)
        try {
            val status = connection.responseCode
            val body = readResponse(connection, status)
            if (status == 401 || status == 403) throw DeviceAuthorizationException()
            if (status == HttpURLConnection.HTTP_NOT_MODIFIED) {
                ZoneStore.updateEtag(this, serverEtag)
                return serverEtag
            }
            if (status !in 200..299) return cachedEtag
            val json = JSONObject(body)
            val etag = json.optString("etag", serverEtag)
            val zones = json.optJSONArray("zones") ?: JSONArray()
            ZoneStore.save(this, etag, zones)
            return etag
        } catch (error: DeviceAuthorizationException) {
            throw error
        } catch (_: Exception) {
            // Zone geometry is optional; never fail the sync because it could not be refreshed.
            return cachedEtag
        } finally {
            connection.disconnect()
        }
    }

    private fun acknowledgeConfig(
        base: String,
        token: String,
        version: Long,
        zonesEtag: String?,
        telemetry: JSONObject? = null,
    ): Boolean {
        val connection = request("$base/v1/config/ack", token, "POST")
        connection.doOutput = true
        connection.setRequestProperty("Content-Type", "application/json")
        try {
            val payload = JSONObject().put("version", version)
            if (!zonesEtag.isNullOrBlank()) payload.put("zones_etag", zonesEtag)
            telemetry?.let { payload.put("telemetry", it) }
            connection.outputStream.use {
                it.write(payload.toString().toByteArray(Charsets.UTF_8))
            }
            val status = connection.responseCode
            val body = readResponse(connection, status)
            if (status == 401 || status == 403) throw DeviceAuthorizationException()
            if (status !in 200..299) throw IllegalStateException("Config acknowledgement failed: $status")
            return JSONObject(body).optBoolean("applied", false)
        } finally {
            connection.disconnect()
        }
    }

    private fun saveConfig(config: TrackerConfig) {
        preferences.edit().putString(PREF_CONFIG, configJSON(config).toString()).apply()
    }

    private fun saveBatteryState(battery: Int, charging: Boolean) {
        val editor = preferences.edit().putBoolean(PREF_CHARGING, charging)
        if (battery in 0..100) editor.putInt(PREF_BATTERY_PERCENT, battery)
        editor.apply()
    }

    private fun queueSize(): Int = try {
        JSONArray(preferences.getString(PREF_QUEUE, "[]")).length()
    } catch (_: Exception) {
        0
    }

    private fun syncDurationMS(): Long =
        (SystemClock.elapsedRealtime() - cycleStartedAt).coerceAtLeast(0L)

    private fun wakeDelaySeconds(): Int? {
        if (scheduledWakeAt <= 0L) return null
        return ((System.currentTimeMillis() - scheduledWakeAt) / 1000L).coerceAtLeast(0L).toInt()
    }

    private fun markSyncSuccess() {
        preferences.edit()
            .putLong(PREF_LAST_SYNC_AT, System.currentTimeMillis())
            .putString(PREF_SYNC_STATUS, "Synced")
            .remove(PREF_SYNC_ERROR)
            .apply()
    }

    private fun markSyncDeferred(message: String) {
        preferences.edit()
            .putLong(PREF_LAST_SYNC_AT, System.currentTimeMillis())
            .putString(PREF_SYNC_STATUS, message)
            .remove(PREF_SYNC_ERROR)
            .apply()
    }

    private fun markSyncFailure(error: Exception) {
        val detail = (error.message ?: "Network unavailable").take(160)
        preferences.edit()
            .putString(PREF_SYNC_STATUS, "Sync failed")
            .putString(PREF_SYNC_ERROR, detail)
            .apply()
    }

    private fun markRevoked() {
        preferences.edit()
            .remove(PREF_TOKEN)
            .putBoolean(PREF_REVOKED, true)
            .putString(PREF_SYNC_STATUS, "Device revoked")
            .putString(PREF_SYNC_ERROR, "The server rejected this device token.")
            .apply()
        stopPermanently()
    }

    private fun syncNow() {
        zoneTransitionDetected = false
        val base = preferences.getString(PREF_SERVER, null)
        val token = preferences.getString(PREF_TOKEN, null)
        if (base.isNullOrBlank() || token.isNullOrBlank()) return stopPermanently()
        preferences.edit().putLong(PREF_LAST_SYNC_ATTEMPT_AT, System.currentTimeMillis())
            .putString(PREF_SYNC_STATUS, "Syncing").remove(PREF_SYNC_ERROR).apply()
        val battery = batteryPercent()
        val charging = isCharging()
        saveBatteryState(battery, charging)

        var updatedConfig: TrackerConfig? = null
        var configAcknowledged = false
        var configError: Exception? = null
        try {
            val fetched = fetchConfig(base, token)
            val config = fetched.config
            updatedConfig = config
            saveConfig(config)
            val zonesEtag = refreshZones(base, token, fetched.zonesEtag)
            configAcknowledged = acknowledgeConfig(
                base,
                token,
                config.version,
                zonesEtag,
                DeviceTelemetry.syncJson(
                    this,
                    config.version,
                    syncDurationMS(),
                    queueSize(),
                    wakeDelaySeconds(),
                ),
            )
            if (!configAcknowledged) configError = IllegalStateException("Server config changed during sync")
        } catch (_: DeviceAuthorizationException) {
            return markRevoked()
        } catch (error: Exception) {
            configError = error
        }

        var uploadError: Exception? = null
        try {
            uploadQueued(base, token)
        } catch (_: DeviceAuthorizationException) {
            return markRevoked()
        } catch (error: Exception) {
            uploadError = error
        }

		if (configError == null && !configAcknowledged) {
			configError = IllegalStateException("Server config changed during sync")
		}
		finishSync(updatedConfig != null, configAcknowledged, configError, uploadError,
			uploadError == null, false)
		stopAfterManualSync(nextWakeDelaySeconds(updatedConfig ?: cachedConfig(), battery, charging))
    }

    private fun request(url: String, token: String, method: String): HttpURLConnection =
        (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = method
            connectTimeout = 15000
            readTimeout = 15000
            setRequestProperty("Authorization", "Bearer $token")
            setRequestProperty("Accept", "application/json")
        }

    private fun readResponse(connection: HttpURLConnection, status: Int): String =
        (if (status in 200..299) connection.inputStream else connection.errorStream)
            ?.bufferedReader()?.use { it.readText() }.orEmpty()

    private fun cachedConfig(): TrackerConfig = try {
        parseConfig(JSONObject(preferences.getString(PREF_CONFIG, "{}")))
    } catch (_: Exception) { TrackerConfig() }

    private fun parseConfig(json: JSONObject) = TrackerConfig(
        enabled = json.optBoolean("enabled", true),
        locationIntervalSeconds = json.optInt("location_interval_seconds", 900).coerceIn(5, 86400),
        syncIntervalSeconds = json.optInt("sync_interval_seconds", 900).coerceIn(5, 86400),
        sleepDurationSeconds = json.optInt("sleep_duration_seconds", 3600).coerceIn(10, 604800),
        gpsTimeoutSeconds = json.optInt("gps_timeout_seconds", 60).coerceIn(5, 180),
        minDistanceMeters = json.optDouble("min_distance_meters", 0.0).toFloat().coerceAtLeast(0f),
        version = json.optLong("version", 1),
    )

    private fun configJSON(config: TrackerConfig) = JSONObject()
        .put("enabled", config.enabled)
        .put("location_interval_seconds", config.locationIntervalSeconds)
        .put("sync_interval_seconds", config.syncIntervalSeconds)
        .put("sleep_duration_seconds", config.sleepDurationSeconds)
        .put("gps_timeout_seconds", config.gpsTimeoutSeconds)
        .put("min_distance_meters", config.minDistanceMeters.toDouble())
        .put("version", config.version)

    private fun batteryPercent(): Int {
        val battery = registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED)) ?: return -1
        val level = battery.getIntExtra(BatteryManager.EXTRA_LEVEL, -1)
        val scale = battery.getIntExtra(BatteryManager.EXTRA_SCALE, -1)
        if (level < 0 || scale <= 0) return -1
        return (level * 100 / scale).coerceIn(0, 100)
    }

    private fun isCharging(): Boolean {
        val battery = registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED)) ?: return false
        val status = battery.getIntExtra(BatteryManager.EXTRA_STATUS, -1)
        return status == BatteryManager.BATTERY_STATUS_CHARGING || status == BatteryManager.BATTERY_STATUS_FULL
    }

    private fun clearLocationListener() {
        for (listener in locationListeners) {
            try { locationManager?.removeUpdates(listener) } catch (_: SecurityException) { }
        }
        locationListeners.clear()
    }

    private fun finishCycle(delaySeconds: Int) {
        synchronized(lifecycleLock) {
            if (!cycleRunning || cycleFinished) return
            cycleFinished = true
            cycleRunning = false
            val stopId = latestStartId
            clearLocationListener()
            wakeLock?.let { if (it.isHeld) it.release() }
            wakeLock = null
            stopForeground(true)
            try {
                // Never schedule an immediate re-fire while this service is still
                // shutting down. The next start must happen after this one stops.
                TrackerScheduler.schedule(this, delaySeconds.coerceAtLeast(1))
            } finally {
                stopSelf(stopId)
            }
        }
    }

    private fun stopPermanently() {
        synchronized(lifecycleLock) {
            if (!cycleRunning || cycleFinished) return
            cycleFinished = true
            cycleRunning = false
            val stopId = latestStartId
            clearLocationListener()
            wakeLock?.let { if (it.isHeld) it.release() }
            wakeLock = null
            try {
                TrackerScheduler.cancel(this)
            } finally {
                stopForeground(true)
                stopSelf(stopId)
            }
        }
    }

    private fun stopAfterManualSync(delaySeconds: Int) {
        synchronized(lifecycleLock) {
            if (!cycleRunning || cycleFinished) return
            cycleFinished = true
            cycleRunning = false
            val stopId = latestStartId
            clearLocationListener()
            wakeLock?.let { if (it.isHeld) it.release() }
            wakeLock = null
            stopForeground(true)
            try {
                TrackerScheduler.schedule(this, delaySeconds.coerceAtLeast(1))
            } finally {
                stopSelf(stopId)
            }
        }
    }

    private fun acquireWakeLock() {
        val manager = getSystemService(Context.POWER_SERVICE) as PowerManager
        wakeLock = manager.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "Kidstracker:location").apply {
            setReferenceCounted(false)
            acquire(5 * 60 * 1000L)
        }
    }

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(CHANNEL_ID, "Location updates", NotificationManager.IMPORTANCE_LOW)
            (getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager).createNotificationChannel(channel)
        }
    }

    private fun notification(): Notification {
        val openApp = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or if (Build.VERSION.SDK_INT >= 23) PendingIntent.FLAG_IMMUTABLE else 0)
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            Notification.Builder(this, CHANNEL_ID).setSmallIcon(android.R.drawable.ic_menu_mylocation)
                .setContentTitle("Kidstracker").setContentText("Checking GPS/network location").setContentIntent(openApp).setOngoing(true).build()
        } else {
            @Suppress("DEPRECATION")
            Notification.Builder(this).setSmallIcon(android.R.drawable.ic_menu_mylocation)
                .setContentTitle("Kidstracker").setContentText("Checking GPS/network location").setContentIntent(openApp).setOngoing(true).build()
        }
    }

    override fun onBind(intent: Intent?): IBinder? = null
    override fun onDestroy() {
        synchronized(lifecycleLock) {
            cycleRunning = false
            cycleFinished = true
        }
        clearLocationListener()
        wakeLock?.let { if (it.isHeld) it.release() }
        executor.shutdown()
        super.onDestroy()
    }

    private class DeviceAuthorizationException : Exception("Device authorization was revoked")

    companion object {
        const val ACTION_SYNC_NOW = "org.kidstracker.client.SYNC_NOW"
        const val PREFERENCES = "tracker"
        const val PREF_SERVER = "server"
        const val PREF_TOKEN = "access_token"
        const val PREF_DEVICE_ID = "device_id"
        const val PREF_CONFIG = "config"
        const val PREF_QUEUE = "location_queue"
        const val PREF_REVOKED = "revoked"
        const val PREF_SYNC_STATUS = "sync_status"
        const val PREF_SYNC_ERROR = "sync_error"
        const val PREF_LAST_SYNC_AT = "last_sync_at"
        const val PREF_LAST_SYNC_ATTEMPT_AT = "last_sync_attempt_at"
        const val PREF_LAST_LOCATION_ATTEMPT_AT = "last_location_attempt_at"
        const val PREF_NEXT_WAKE = "next_wake"
        const val PREF_LAST_LOCATION_AT = "last_location_at"
        const val PREF_LAST_LOCATION_SOURCE = "last_location_source"
        private const val FUSED_PROVIDER = "fused"
        const val PREF_BATTERY_PERCENT = "battery_percent"
        const val PREF_CHARGING = "charging"
        const val PREF_LAST_LAT = "last_latitude"
        const val PREF_LAST_LON = "last_longitude"
        private const val MAX_QUEUED_LOCATIONS = 500
        private const val CHANNEL_ID = "location-checks"
        private const val NOTIFICATION_ID = 1001
        private const val WATCHDOG_DELAY_SECONDS = 300
    }
}
