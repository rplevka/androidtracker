package org.kidstracker.client

data class TrackerConfig(
    val enabled: Boolean = true,
    val locationIntervalSeconds: Int = 900,
    val syncIntervalSeconds: Int = 900,
    val sleepDurationSeconds: Int = 3600,
    val gpsTimeoutSeconds: Int = 60,
    val minDistanceMeters: Float = 0f,
    val version: Long = 1,
)

object CyclePolicy {
    fun effectiveIntervalSeconds(intervalSeconds: Int, batteryPercent: Int, charging: Boolean): Int {
        val multiplier = when {
            charging || batteryPercent !in 0..100 -> 1
            batteryPercent <= 15 -> 4
            batteryPercent <= 30 -> 2
            else -> 1
        }
        val interval = (intervalSeconds.coerceIn(5, 86400).toLong() * multiplier)
            .coerceAtMost(86400L).toInt()
        return if (!charging && batteryPercent in 0..5) maxOf(interval, 3600) else interval
    }

    fun shouldRecord(distanceMeters: Float, minimumDistanceMeters: Float): Boolean =
        minimumDistanceMeters <= 0f || distanceMeters >= minimumDistanceMeters
}
