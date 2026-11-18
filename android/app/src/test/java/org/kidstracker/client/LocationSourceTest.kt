package org.kidstracker.client

import org.junit.Assert.assertEquals
import org.junit.Test

class LocationSourceTest {
    @Test
    fun mapsAndroidProvidersToStableWireValues() {
        assertEquals("gps", LocationSource.fromProvider("gps"))
        assertEquals("network", LocationSource.fromProvider("network"))
        assertEquals("gps", LocationSource.fromProvider(" GPS "))
        assertEquals("unknown", LocationSource.fromProvider(null))
        assertEquals("unknown", LocationSource.fromProvider(""))
    }

    @Test
    fun labelsFallbackClearly() {
        assertEquals("GPS", LocationSource.label("gps"))
        assertEquals("network (cell/Wi-Fi)", LocationSource.label("network"))
        assertEquals("unknown", LocationSource.label(null))
    }
}
