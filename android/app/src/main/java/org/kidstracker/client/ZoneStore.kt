package org.kidstracker.client

import android.content.Context
import android.location.Location
import org.json.JSONArray
import org.json.JSONObject

data class GeoPoint(val latitude: Double, val longitude: Double)

data class ZonePolygon(
    val outer: List<GeoPoint>,
    val holes: List<List<GeoPoint>>,
)

data class TrackerZone(
    val id: String,
    val name: String,
    val priority: Int,
    val polygons: List<ZonePolygon>,
)

data class ZoneEvaluation(
    val states: Map<String, String>,
    val transitions: List<TrackerZone>,
)

object ZoneGeofence {
    const val STATE_INSIDE = "inside"
    const val STATE_OUTSIDE = "outside"

    fun parseZones(zonesJson: JSONArray): List<TrackerZone> {
        val zones = mutableListOf<TrackerZone>()
        for (index in 0 until zonesJson.length()) {
            val item = zonesJson.optJSONObject(index) ?: continue
            val id = item.optString("id").trim()
            if (id.isEmpty()) continue
            val geometry = item.optJSONObject("geometry") ?: continue
            val polygons = parseGeometry(geometry)
            if (polygons.isEmpty()) continue
            zones.add(
                TrackerZone(
                    id = id,
                    name = item.optString("name", id),
                    priority = item.optInt("priority", 0),
                    polygons = polygons,
                )
            )
        }
        return zones.sortedByDescending { it.priority }
    }

    private fun parseGeometry(geometry: JSONObject): List<ZonePolygon> {
        val coordinates = geometry.optJSONArray("coordinates") ?: return emptyList()
        return when (geometry.optString("type")) {
            "Polygon" -> listOfNotNull(parsePolygon(coordinates))
            "MultiPolygon" -> (0 until coordinates.length()).mapNotNull { index ->
                coordinates.optJSONArray(index)?.let(::parsePolygon)
            }
            else -> emptyList()
        }
    }

    private fun parsePolygon(polygonCoordinates: JSONArray): ZonePolygon? {
        if (polygonCoordinates.length() == 0) return null
        val outer = parseRing(polygonCoordinates.optJSONArray(0)) ?: return null
        val holes = (1 until polygonCoordinates.length()).mapNotNull { index ->
            parseRing(polygonCoordinates.optJSONArray(index))
        }
        return ZonePolygon(outer, holes)
    }

    private fun parseRing(ringJson: JSONArray?): List<GeoPoint>? {
        if (ringJson == null || ringJson.length() < 3) return null
        val points = ArrayList<GeoPoint>(ringJson.length())
        for (index in 0 until ringJson.length()) {
            val pair = ringJson.optJSONArray(index) ?: continue
            if (pair.length() < 2) continue
            val longitude = pair.optDouble(0, Double.NaN)
            val latitude = pair.optDouble(1, Double.NaN)
            if (latitude.isFinite() && longitude.isFinite()) {
                points.add(GeoPoint(latitude, longitude))
            }
        }
        return if (points.size >= 3) points else null
    }

    fun contains(zone: TrackerZone, latitude: Double, longitude: Double): Boolean =
        zone.polygons.any { polygon ->
            pointInRing(latitude, longitude, polygon.outer) &&
                polygon.holes.none { hole -> pointInRing(latitude, longitude, hole) }
        }

    private fun pointInRing(latitude: Double, longitude: Double, ring: List<GeoPoint>): Boolean {
        if (ring.size < 3) return false
        var inside = false
        var previous = ring.last()
        for (current in ring) {
            val crossesLatitude = (current.latitude > latitude) != (previous.latitude > latitude)
            if (crossesLatitude) {
                val longitudeAtLatitude = (previous.longitude - current.longitude) *
                    (latitude - current.latitude) / (previous.latitude - current.latitude) +
                    current.longitude
                if (longitude < longitudeAtLatitude) inside = !inside
            }
            previous = current
        }
        return inside
    }

    fun evaluate(
        zones: List<TrackerZone>,
        previousStates: Map<String, String>,
        previousPoint: GeoPoint?,
        currentPoint: GeoPoint,
    ): ZoneEvaluation {
        val states = LinkedHashMap<String, String>(zones.size)
        val transitions = mutableListOf<TrackerZone>()
        for (zone in zones) {
            val currentInside = contains(zone, currentPoint.latitude, currentPoint.longitude)
            val currentState = if (currentInside) STATE_INSIDE else STATE_OUTSIDE
            val previousState = previousStates[zone.id]
            if (previousState == null) {
                // Seed the first state from the previous fix so a fresh zone cache
                // does not generate a false transition.
                val baselineInside = previousPoint?.let {
                    contains(zone, it.latitude, it.longitude)
                } ?: currentInside
                if (previousPoint != null && baselineInside != currentInside) {
                    transitions.add(zone)
                }
            } else if (previousState != currentState) {
                transitions.add(zone)
            }
            states[zone.id] = currentState
        }
        return ZoneEvaluation(states, transitions)
    }
}

object ZoneStore {
    private const val PREF_ZONES_JSON = "zones_json"
    private const val PREF_ZONES_ETAG = "zones_etag"
    private const val PREF_ZONE_STATES = "zone_states"

    private fun preferences(context: Context) =
        context.getSharedPreferences(TrackerService.PREFERENCES, Context.MODE_PRIVATE)

    fun cachedEtag(context: Context): String? =
        preferences(context).getString(PREF_ZONES_ETAG, null)

    fun cachedZones(context: Context): List<TrackerZone> = try {
        val raw = preferences(context).getString(PREF_ZONES_JSON, null) ?: return emptyList()
        ZoneGeofence.parseZones(JSONArray(raw))
    } catch (_: Exception) {
        emptyList()
    }

    fun save(context: Context, etag: String?, zones: JSONArray) {
        val prefs = preferences(context)
        val previousEtag = prefs.getString(PREF_ZONES_ETAG, null)
        val editor = prefs.edit()
            .putString(PREF_ZONES_JSON, zones.toString())
            .putString(PREF_ZONES_ETAG, etag)
        if (previousEtag != etag) editor.remove(PREF_ZONE_STATES)
        editor.apply()
    }

    fun updateEtag(context: Context, etag: String?) {
        preferences(context).edit().putString(PREF_ZONES_ETAG, etag).apply()
    }

    fun evaluate(context: Context, location: Location): Boolean = try {
        val zones = cachedZones(context)
        if (zones.isEmpty()) {
            false
        } else {
            val prefs = preferences(context)
            val previousStates = parseStates(prefs.getString(PREF_ZONE_STATES, null))
            val previousLatitude = prefs.getFloat(TrackerService.PREF_LAST_LAT, Float.NaN)
            val previousLongitude = prefs.getFloat(TrackerService.PREF_LAST_LON, Float.NaN)
            val previousPoint = if (previousLatitude.isFinite() && previousLongitude.isFinite()) {
                GeoPoint(previousLatitude.toDouble(), previousLongitude.toDouble())
            } else {
                null
            }
            val result = ZoneGeofence.evaluate(
                zones = zones,
                previousStates = previousStates,
                previousPoint = previousPoint,
                currentPoint = GeoPoint(location.latitude, location.longitude),
            )
            saveStates(prefs, result.states)
            result.transitions.isNotEmpty()
        }
    } catch (_: Exception) {
        // Zone evaluation is an optimization; never fail location tracking because
        // cached geometry or state is malformed.
        false
    }

    private fun parseStates(raw: String?): Map<String, String> {
        if (raw.isNullOrBlank()) return emptyMap()
        val json = JSONObject(raw)
        val states = LinkedHashMap<String, String>(json.length())
        for (key in json.keys()) {
            val value = json.optString(key)
            if (value == ZoneGeofence.STATE_INSIDE || value == ZoneGeofence.STATE_OUTSIDE) {
                states[key] = value
            }
        }
        return states
    }

    private fun saveStates(
        preferences: android.content.SharedPreferences,
        states: Map<String, String>,
    ) {
        val json = JSONObject()
        for ((zoneId, state) in states) json.put(zoneId, state)
        preferences.edit().putString(PREF_ZONE_STATES, json.toString()).apply()
    }
}
