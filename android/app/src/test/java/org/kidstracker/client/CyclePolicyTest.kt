package org.kidstracker.client

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class CyclePolicyTest {
    @Test
    fun supportsSeparateMinimumLocationSyncAndDisabledIntervals() {
        assertEquals(5, CyclePolicy.effectiveIntervalSeconds(5, 70, false))
        assertEquals(5, CyclePolicy.effectiveIntervalSeconds(5, 70, true))
        assertEquals(10, CyclePolicy.effectiveIntervalSeconds(10, 70, false))
    }

    @Test
    fun lowBatteryReducesGpsWakeupsAndChargingRestoresCadence() {
        assertEquals(1800, CyclePolicy.effectiveIntervalSeconds(900, 25, false))
        assertEquals(3600, CyclePolicy.effectiveIntervalSeconds(900, 12, false))
        assertEquals(3600, CyclePolicy.effectiveIntervalSeconds(900, 4, false))
        assertEquals(900, CyclePolicy.effectiveIntervalSeconds(900, 12, true))
    }

    @Test
    fun distanceFilterCanBeDisabledOrApplied() {
        assertTrue(CyclePolicy.shouldRecord(0f, 0f))
        assertFalse(CyclePolicy.shouldRecord(15f, 50f))
        assertTrue(CyclePolicy.shouldRecord(50f, 50f))
    }
}
