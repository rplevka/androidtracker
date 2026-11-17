package org.kidstracker.client

import android.app.KeyguardManager
import android.app.usage.UsageStatsManager
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.location.LocationManager
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.os.BatteryManager
import android.os.Build
import android.os.PowerManager
import org.json.JSONObject
import java.time.Instant

object DeviceTelemetry {
    data class Snapshot(
        val batteryPercent: Int?,
        val charging: Boolean,
        val screenOn: Boolean,
        val deviceLocked: Boolean,
        val deviceIdle: Boolean,
        val powerSaveMode: Boolean,
        val standbyBucket: String,
        val networkType: String,
        val gpsEnabled: Boolean,
        val batteryTemperatureC: Double?,
    )

    fun snapshot(context: Context): Snapshot {
        val power = context.getSystemService(Context.POWER_SERVICE) as PowerManager
        val keyguard = context.getSystemService(Context.KEYGUARD_SERVICE) as KeyguardManager
        val battery = context.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
        val level = battery?.getIntExtra(BatteryManager.EXTRA_LEVEL, -1) ?: -1
        val scale = battery?.getIntExtra(BatteryManager.EXTRA_SCALE, -1) ?: -1
        val batteryPercent = if (level in 0..100 && scale > 0) (level * 100 / scale).coerceIn(0, 100) else null
        val status = battery?.getIntExtra(BatteryManager.EXTRA_STATUS, -1) ?: -1
        val temperature = battery?.getIntExtra(BatteryManager.EXTRA_TEMPERATURE, Int.MIN_VALUE)
        val gpsEnabled = try {
            (context.getSystemService(Context.LOCATION_SERVICE) as LocationManager)
                .isProviderEnabled(LocationManager.GPS_PROVIDER)
        } catch (_: Exception) {
            false
        }
        return Snapshot(
            batteryPercent = batteryPercent,
            charging = status == BatteryManager.BATTERY_STATUS_CHARGING || status == BatteryManager.BATTERY_STATUS_FULL,
            screenOn = power.isInteractive,
            deviceLocked = keyguard.isKeyguardLocked,
            deviceIdle = Build.VERSION.SDK_INT >= Build.VERSION_CODES.M && power.isDeviceIdleMode,
            powerSaveMode = power.isPowerSaveMode,
            standbyBucket = standbyBucket(context),
            networkType = networkType(context),
            gpsEnabled = gpsEnabled,
            batteryTemperatureC = temperature?.takeUnless { it == Int.MIN_VALUE }?.div(10.0),
        )
    }

    fun syncJson(
        context: Context,
        configVersion: Long,
        syncDurationMs: Long,
        queueSize: Int,
        wakeDelaySeconds: Int?,
    ): JSONObject {
        val snapshot = snapshot(context)
        return JSONObject().apply {
            put("recorded_at", Instant.now().toString())
            snapshot.batteryPercent?.let { put("battery_percent", it) }
            put("charging", snapshot.charging)
            put("screen_on", snapshot.screenOn)
            put("device_locked", snapshot.deviceLocked)
            put("device_idle", snapshot.deviceIdle)
            put("power_save_mode", snapshot.powerSaveMode)
            put("standby_bucket", snapshot.standbyBucket)
            put("network_type", snapshot.networkType)
            put("gps_enabled", snapshot.gpsEnabled)
            snapshot.batteryTemperatureC?.let { put("battery_temperature_c", it) }
            wakeDelaySeconds?.let { put("wake_delay_seconds", it) }
            put("sync_duration_ms", syncDurationMs)
            put("queue_size", queueSize)
            put("config_version", configVersion)
        }
    }

    fun addLocationFields(context: Context, item: JSONObject) {
        val snapshot = snapshot(context)
        item.put("screen_on", snapshot.screenOn)
        item.put("device_locked", snapshot.deviceLocked)
        item.put("device_idle", snapshot.deviceIdle)
        item.put("power_save_mode", snapshot.powerSaveMode)
        item.put("standby_bucket", snapshot.standbyBucket)
        item.put("network_type", snapshot.networkType)
    }

    private fun standbyBucket(context: Context): String {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.P) return "unsupported"
        return try {
            val manager = context.getSystemService(UsageStatsManager::class.java) ?: return "unknown"
            when (manager.appStandbyBucket) {
                UsageStatsManager.STANDBY_BUCKET_ACTIVE -> "active"
                UsageStatsManager.STANDBY_BUCKET_WORKING_SET -> "working_set"
                UsageStatsManager.STANDBY_BUCKET_FREQUENT -> "frequent"
                UsageStatsManager.STANDBY_BUCKET_RARE -> "rare"
                UsageStatsManager.STANDBY_BUCKET_RESTRICTED -> "restricted"
                5 -> "exempted"
                50 -> "never"
                else -> "unknown"
            }
        } catch (_: Exception) {
            "unknown"
        }
    }

    private fun networkType(context: Context): String = try {
        val manager = context.getSystemService(ConnectivityManager::class.java) ?: return "unknown"
        val network = manager.activeNetwork ?: return "offline"
        val capabilities = manager.getNetworkCapabilities(network) ?: return "unknown"
        when {
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "cellular"
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
            capabilities.hasTransport(NetworkCapabilities.TRANSPORT_VPN) -> "vpn"
            else -> "other"
        }
    } catch (_: Exception) {
        "unknown"
    }
}
