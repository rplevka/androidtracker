package org.kidstracker.client

object LocationSource {
    const val GPS = "gps"
    const val NETWORK = "network"
    const val UNKNOWN = "unknown"

    fun fromProvider(provider: String?): String {
        val normalized = provider?.trim()?.lowercase().orEmpty()
        return when (normalized) {
            GPS -> GPS
            NETWORK -> NETWORK
            "" -> UNKNOWN
            else -> normalized.take(32)
        }
    }

    fun label(source: String?): String = when (source) {
        GPS -> "GPS"
        NETWORK -> "network (cell/Wi-Fi)"
        null, "" -> "unknown"
        else -> source
    }
}
