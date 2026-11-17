package org.kidstracker.client

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.os.Build

class TrackerReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != ACTION_TRACK) return
        val service = Intent(context, TrackerService::class.java)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) context.startForegroundService(service)
        else context.startService(service)
    }

    companion object { const val ACTION_TRACK = "org.kidstracker.client.TRACK" }
}

class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        val preferences = context.getSharedPreferences(TrackerService.PREFERENCES, Context.MODE_PRIVATE)
        if (preferences.getString(TrackerService.PREF_TOKEN, null) != null) TrackerScheduler.schedule(context, 0)
    }
}
