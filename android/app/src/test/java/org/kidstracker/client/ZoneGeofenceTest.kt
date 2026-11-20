package org.kidstracker.client

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ZoneGeofenceTest {
    private fun square(
        outer: List<GeoPoint> = listOf(
            GeoPoint(0.0, 0.0),
            GeoPoint(0.0, 1.0),
            GeoPoint(1.0, 1.0),
            GeoPoint(1.0, 0.0),
        ),
        holes: List<List<GeoPoint>> = emptyList(),
    ) = TrackerZone(
        id = "school",
        name = "School",
        priority = 0,
        polygons = listOf(ZonePolygon(outer, holes)),
    )

    @Test
    fun pointInPolygonAndHole() {
        val outer = square()
        assertTrue(ZoneGeofence.contains(outer, 0.5, 0.5))
        assertFalse(ZoneGeofence.contains(outer, 2.0, 2.0))

        val withHole = square(
            holes = listOf(
                listOf(
                    GeoPoint(0.4, 0.4),
                    GeoPoint(0.4, 0.6),
                    GeoPoint(0.6, 0.6),
                    GeoPoint(0.6, 0.4),
                )
            )
        )
        assertFalse(ZoneGeofence.contains(withHole, 0.5, 0.5))
        assertTrue(ZoneGeofence.contains(withHole, 0.2, 0.2))
    }

    @Test
    fun detectsTransitionsAndSeedsFromPreviousFix() {
        val zone = square()
        val inside = GeoPoint(0.5, 0.5)
        val outside = GeoPoint(2.0, 2.0)

        val transition = ZoneGeofence.evaluate(
            zones = listOf(zone),
            previousStates = mapOf("school" to ZoneGeofence.STATE_INSIDE),
            previousPoint = inside,
            currentPoint = outside,
        )
        assertEquals(1, transition.transitions.size)
        assertEquals(ZoneGeofence.STATE_OUTSIDE, transition.states["school"])

        val seededTransition = ZoneGeofence.evaluate(
            zones = listOf(zone),
            previousStates = emptyMap(),
            previousPoint = inside,
            currentPoint = outside,
        )
        assertEquals(1, seededTransition.transitions.size)

        val noTransition = ZoneGeofence.evaluate(
            zones = listOf(zone),
            previousStates = mapOf("school" to ZoneGeofence.STATE_OUTSIDE),
            previousPoint = outside,
            currentPoint = outside,
        )
        assertTrue(noTransition.transitions.isEmpty())
    }
}
