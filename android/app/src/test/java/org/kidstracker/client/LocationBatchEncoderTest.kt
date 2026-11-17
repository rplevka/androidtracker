package org.kidstracker.client

import java.io.ByteArrayInputStream
import java.util.zip.GZIPInputStream
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class LocationBatchEncoderTest {
    @Test
    fun gzipRoundTripsJsonAndReducesRepeatedLocationPayload() {
        val payload = (1..20).joinToString(prefix = "{\"locations\":[", postfix = "]}", separator = ",") {
            "{\"id\":\"123e4567-e89b-12d3-a456-426614174000\",\"latitude\":51.5,\"longitude\":-0.12,\"accuracy_meters\":8,\"recorded_at\":\"2026-11-15T12:00:00Z\"}"
        }.toByteArray(Charsets.UTF_8)
        val compressed = LocationBatchEncoder.gzip(payload)
        val restored = GZIPInputStream(ByteArrayInputStream(compressed)).use { it.readBytes() }
        assertArrayEquals(payload, restored)
        assertTrue("repeated fields should compress", compressed.size < payload.size)
    }
}
