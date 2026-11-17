package org.kidstracker.client

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.view.View
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import org.json.JSONObject
import org.json.JSONArray
import com.google.zxing.integration.android.IntentIntegrator
import java.net.HttpURLConnection
import java.net.URL
import java.text.DateFormat
import java.util.Date
import java.util.concurrent.Executors

class MainActivity : Activity() {
    private val executor = Executors.newSingleThreadExecutor()
    private lateinit var serverField: EditText
    private lateinit var codeField: EditText
    private lateinit var enrollmentStatus: TextView
    private lateinit var statusDetails: TextView
    private lateinit var setupTab: Button
    private lateinit var statusTab: Button
    private lateinit var setupPanel: LinearLayout
    private lateinit var statusPanel: LinearLayout
    private lateinit var syncButton: Button
    private var selectedTab = TAB_SETUP
    private val preferences by lazy { getSharedPreferences(TrackerService.PREFERENCES, MODE_PRIVATE) }
    private val preferenceListener = android.content.SharedPreferences.OnSharedPreferenceChangeListener { _, _ ->
        runOnUiThread { renderUi() }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val padding = (20 * resources.displayMetrics.density).toInt()
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(padding, padding, padding, padding)
        }

        root.addView(TextView(this).apply {
            text = "Kidstracker"
            textSize = 22f
        }, match())

        val tabs = LinearLayout(this).apply { orientation = LinearLayout.HORIZONTAL }
        setupTab = Button(this).apply {
            text = "Enrollment"
            setOnClickListener { selectedTab = TAB_SETUP; renderUi() }
        }
        statusTab = Button(this).apply {
            text = "Status"
            setOnClickListener { selectedTab = TAB_STATUS; renderUi() }
        }
        tabs.addView(setupTab, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        tabs.addView(statusTab, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        root.addView(tabs, match())

        setupPanel = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(0, padding / 2, 0, 0)
        }
        setupPanel.addView(TextView(this).apply {
            text = "Use an HTTPS server URL and the one-time enrollment code from the admin page."
            textSize = 16f
        }, match())
        serverField = EditText(this).apply {
            hint = "https://tracker.example.com"
            setSingleLine(true)
            setText(preferences.getString(TrackerService.PREF_SERVER, ""))
            inputType = android.text.InputType.TYPE_CLASS_TEXT or android.text.InputType.TYPE_TEXT_VARIATION_URI
        }
        setupPanel.addView(serverField, match())
        codeField = EditText(this).apply {
            hint = "One-time enrollment code"
            setSingleLine(true)
        }
        setupPanel.addView(codeField, match())
        val scanQr = Button(this).apply {
            text = "Scan enrollment QR"
            setOnClickListener { requestCameraPermissionOrScan() }
        }
        setupPanel.addView(scanQr, match())
        val activate = Button(this).apply {
            text = "Activate tracker"
            setOnClickListener { requestPermissionOrEnroll() }
        }
        setupPanel.addView(activate, match())
        enrollmentStatus = TextView(this).apply { text = statusText() }
        setupPanel.addView(enrollmentStatus, match())
        root.addView(setupPanel, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))

        statusPanel = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(0, padding / 2, 0, 0)
        }
        syncButton = Button(this).apply {
            text = "Sync now"
            setOnClickListener { forceSync() }
        }
        statusPanel.addView(syncButton, match())
        statusDetails = TextView(this).apply { textSize = 16f }
        val scroll = ScrollView(this).apply { addView(statusDetails) }
        statusPanel.addView(scroll, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        root.addView(statusPanel, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))

        setContentView(root)
        renderUi()
    }

    private fun match() = LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT)

    override fun onResume() {
        super.onResume()
        preferences.registerOnSharedPreferenceChangeListener(preferenceListener)
        renderUi()
    }

    override fun onPause() {
        preferences.unregisterOnSharedPreferenceChangeListener(preferenceListener)
        super.onPause()
    }

    private fun renderUi() {
        val activated = !preferences.getString(TrackerService.PREF_TOKEN, null).isNullOrBlank()
        if (activated) selectedTab = TAB_STATUS
        else if (preferences.getBoolean(TrackerService.PREF_REVOKED, false)) selectedTab = TAB_SETUP

        setupTab.visibility = if (activated) View.GONE else View.VISIBLE
        setupTab.isSelected = selectedTab == TAB_SETUP
        statusTab.isSelected = selectedTab == TAB_STATUS
        setupPanel.visibility = if (!activated && selectedTab == TAB_SETUP) View.VISIBLE else View.GONE
        statusPanel.visibility = if (activated || selectedTab == TAB_STATUS) View.VISIBLE else View.GONE
        if (!activated) enrollmentStatus.text = statusText()
        renderStatus(activated)
    }

    private fun renderStatus(activated: Boolean) {
        val syncStatus = preferences.getString(TrackerService.PREF_SYNC_STATUS, null)
            ?: if (activated) "Waiting for first sync" else "Not activated"
        syncButton.visibility = if (activated) View.VISIBLE else View.GONE
        syncButton.isEnabled = activated && syncStatus != "Syncing"
        syncButton.text = if (syncStatus == "Syncing") "Syncing…" else "Sync now"

        val config = try {
            JSONObject(preferences.getString(TrackerService.PREF_CONFIG, "{}").orEmpty())
        } catch (_: Exception) {
            JSONObject()
        }
        val queueSize = try { JSONArray(preferences.getString(TrackerService.PREF_QUEUE, "[]")).length() }
        catch (_: Exception) { 0 }
        val battery = preferences.getInt(TrackerService.PREF_BATTERY_PERCENT, -1)
        val charging = preferences.getBoolean(TrackerService.PREF_CHARGING, false)
        val lastSync = preferences.getLong(TrackerService.PREF_LAST_SYNC_AT, 0L)
        val lastLocation = preferences.getLong(TrackerService.PREF_LAST_LOCATION_AT, 0L)
        val nextWake = preferences.getLong(TrackerService.PREF_NEXT_WAKE, 0L)

        statusDetails.text = buildString {
            appendLine("Status: ${if (activated) "Activated" else statusText()}")
            val deviceId = preferences.getString(TrackerService.PREF_DEVICE_ID, null)
            if (!deviceId.isNullOrBlank()) appendLine("Device: $deviceId")
            val server = preferences.getString(TrackerService.PREF_SERVER, null)
            if (!server.isNullOrBlank()) appendLine("Server: $server")
            if (activated) {
                appendLine("Tracking: ${if (config.optBoolean("enabled", true)) "Enabled" else "Disabled"}")
                appendLine("Location interval: ${config.optInt("location_interval_seconds", 900)} seconds")
                appendLine("Sync/config interval: ${config.optInt("sync_interval_seconds", 900)} seconds")
                appendLine("Disabled sleep/poll: ${config.optInt("sleep_duration_seconds", 3600)} seconds")
                appendLine("GPS timeout: ${config.optInt("gps_timeout_seconds", 60)} seconds")
                appendLine("Minimum distance: ${config.optDouble("min_distance_meters", 0.0)} m")
                appendLine("Config version: ${config.optLong("version", 1)}")
            }
            appendLine("Battery: ${if (battery in 0..100) "$battery%" else "unknown"}${if (charging) " (charging)" else ""}")
            val snapshot = DeviceTelemetry.snapshot(this@MainActivity)
            appendLine("Screen: ${if (snapshot.screenOn) "on" else "off"} · Locked: ${if (snapshot.deviceLocked) "yes" else "no"}")
            appendLine("Doze: ${if (snapshot.deviceIdle) "yes" else "no"} · Power save: ${if (snapshot.powerSaveMode) "on" else "off"}")
            appendLine("Network: ${snapshot.networkType} · Standby: ${snapshot.standbyBucket}")
            appendLine("Last sync: ${formatTime(lastSync)}")
            appendLine("Sync status: $syncStatus")
            val error = preferences.getString(TrackerService.PREF_SYNC_ERROR, null)
            if (!error.isNullOrBlank()) appendLine("Last error: $error")
            appendLine("Queued locations: $queueSize")
            appendLine("Last location: ${formatTime(lastLocation)}")
            append("Next scheduled wake: ${formatTime(nextWake)}")
        }
    }

    private fun formatTime(epochMillis: Long): String =
        if (epochMillis <= 0L) "Never" else DateFormat.getDateTimeInstance().format(Date(epochMillis))

    private fun forceSync() {
        if (preferences.getString(TrackerService.PREF_TOKEN, null).isNullOrBlank()) {
            selectedTab = TAB_SETUP
            renderUi()
            return
        }
        preferences.edit().putString(TrackerService.PREF_SYNC_STATUS, "Syncing").remove(TrackerService.PREF_SYNC_ERROR).apply()
        val service = Intent(this, TrackerService::class.java).setAction(TrackerService.ACTION_SYNC_NOW)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) startForegroundService(service) else startService(service)
    }

    private fun requestPermissionOrEnroll() {
        if (checkSelfPermission(Manifest.permission.ACCESS_FINE_LOCATION) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.ACCESS_FINE_LOCATION), REQUEST_LOCATION)
        } else {
            enroll()
        }
    }

    private fun requestCameraPermissionOrScan() {
        if (checkSelfPermission(Manifest.permission.CAMERA) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(arrayOf(Manifest.permission.CAMERA), REQUEST_CAMERA)
        } else {
            launchQrScanner()
        }
    }

    private fun launchQrScanner() {
        IntentIntegrator(this).apply {
            setDesiredBarcodeFormats(IntentIntegrator.QR_CODE)
            setPrompt("Scan the enrollment QR from the admin dashboard")
            setBeepEnabled(false)
            setOrientationLocked(false)
        }.initiateScan()
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        when (requestCode) {
            REQUEST_LOCATION -> if (grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) enroll()
                else enrollmentStatus.text = "Location permission is required for tracking."
            REQUEST_CAMERA -> if (grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) launchQrScanner()
                else enrollmentStatus.text = "Camera permission is required to scan enrollment QR codes."
        }
    }

    @Deprecated("Deprecated by Android; retained for API 28 and ZXing Activity Result support")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        val result = IntentIntegrator.parseActivityResult(requestCode, resultCode, data)
        if (result != null) {
            val contents = result.contents
            if (contents == null) {
                enrollmentStatus.text = "QR scan cancelled."
            } else {
                applyEnrollmentQr(contents)
            }
            return
        }
        super.onActivityResult(requestCode, resultCode, data)
    }

    private fun applyEnrollmentQr(contents: String) {
        try {
            codeField.setText(EnrollmentQr.parseCode(contents))
            enrollmentStatus.text = if (serverField.text.isNullOrBlank()) {
                "QR accepted. Enter the HTTPS server URL, then tap Activate tracker."
            } else {
                "QR accepted. Review the server URL and tap Activate tracker."
            }
        } catch (error: Exception) {
            enrollmentStatus.text = error.message ?: "Invalid enrollment QR code."
        }
    }

    private fun enroll() {
        val base = serverField.text.toString().trim().trimEnd('/')
        val code = codeField.text.toString().trim()
        if (!base.startsWith("https://") || code.length < 20) {
            enrollmentStatus.text = "Enter an HTTPS URL and a valid enrollment code."
            return
        }
        enrollmentStatus.text = "Activating…"
        executor.execute {
            try {
                val connection = (URL("$base/v1/enroll").openConnection() as HttpURLConnection).apply {
                    requestMethod = "POST"
                    connectTimeout = 15000
                    readTimeout = 15000
                    doOutput = true
                    setRequestProperty("Content-Type", "application/json")
                }
                connection.outputStream.use { it.write(JSONObject().put("enrollment_code", code).toString().toByteArray()) }
                val responseCode = connection.responseCode
                val body = (if (responseCode in 200..299) connection.inputStream else connection.errorStream)
                    ?.bufferedReader()?.use { it.readText() }.orEmpty()
                connection.disconnect()
                if (responseCode !in 200..299) throw IllegalStateException("Activation failed ($responseCode). Check code and server URL.")
                val response = JSONObject(body)
                preferences.edit()
                    .putString(TrackerService.PREF_SERVER, base)
                    .putString(TrackerService.PREF_TOKEN, response.getString("access_token"))
                    .putString(TrackerService.PREF_DEVICE_ID, response.getString("device_id"))
                    .putString(TrackerService.PREF_CONFIG, response.getJSONObject("config").toString())
                    .putBoolean(TrackerService.PREF_REVOKED, false)
                    .putLong(TrackerService.PREF_LAST_SYNC_ATTEMPT_AT, 0L)
                    .putLong(TrackerService.PREF_LAST_LOCATION_ATTEMPT_AT, 0L)
                    .remove(TrackerService.PREF_LAST_SYNC_AT)
                    .apply()
                TrackerScheduler.schedule(this, 0)
                runOnUiThread { renderUi() }
            } catch (error: Exception) {
                runOnUiThread { enrollmentStatus.text = error.message ?: "Activation failed." }
            }
        }
    }

    private fun statusText(): String = if (preferences.getString(TrackerService.PREF_TOKEN, null) == null) {
        if (preferences.getBoolean(TrackerService.PREF_REVOKED, false)) "Device access was revoked by the administrator."
        else "Not activated. Location is only collected after activation."
    } else {
        "Device ${preferences.getString(TrackerService.PREF_DEVICE_ID, "")} is activated."
    }

    override fun onDestroy() {
        executor.shutdown()
        super.onDestroy()
    }

    companion object {
        private const val REQUEST_LOCATION = 41
        private const val REQUEST_CAMERA = 42
        private const val TAB_SETUP = 0
        private const val TAB_STATUS = 1
    }
}
