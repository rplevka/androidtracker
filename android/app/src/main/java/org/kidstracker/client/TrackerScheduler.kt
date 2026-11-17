package org.kidstracker.client

import android.app.AlarmManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build

object TrackerScheduler {
    private fun pendingIntent(context: Context): PendingIntent {
        val intent = Intent(context, TrackerReceiver::class.java).setAction(TrackerReceiver.ACTION_TRACK)
        val flags = PendingIntent.FLAG_UPDATE_CURRENT or if (Build.VERSION.SDK_INT >= 23) PendingIntent.FLAG_IMMUTABLE else 0
        return PendingIntent.getBroadcast(context, 1, intent, flags)
    }

    fun schedule(context: Context, delaySeconds: Int) {
        val alarm = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
        val whenMillis = System.currentTimeMillis() + (delaySeconds.coerceAtLeast(0) * 1000L)
        context.getSharedPreferences(TrackerService.PREFERENCES, Context.MODE_PRIVATE).edit()
            .putLong(TrackerService.PREF_NEXT_WAKE, whenMillis).apply()
        alarm.setAndAllowWhileIdle(AlarmManager.RTC_WAKEUP, whenMillis, pendingIntent(context))
    }

    fun cancel(context: Context) {
        val alarm = context.getSystemService(Context.ALARM_SERVICE) as AlarmManager
        alarm.cancel(pendingIntent(context))
        context.getSharedPreferences(TrackerService.PREFERENCES, Context.MODE_PRIVATE).edit()
            .remove(TrackerService.PREF_NEXT_WAKE).apply()
    }
}
