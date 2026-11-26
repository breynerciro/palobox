package org.dpdns.palobox.twa;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.Service;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.media.AudioManager;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;

/**
 * Servicio en primer plano mientras la música suena con la app en el fondo: sin él el sistema congela el proceso
 * y la música se corta. No reproduce nada: el audio sigue siendo el del WebView.
 */
public class PlaybackService extends Service {
    private static final String CHANNEL = "playback";
    private final Handler handler = new Handler(Looper.getMainLooper());
    private int quiet; // revisiones seguidas sin música

    // Pausada desde otro dispositivo, la app ya no tiene por qué quedarse viva: a los dos minutos en silencio se suelta.
    private final Runnable check = new Runnable() {
        @Override
        public void run() {
            quiet = ((AudioManager) getSystemService(AUDIO_SERVICE)).isMusicActive() ? 0 : quiet + 1;
            if (quiet >= 2) stopSelf();
            else handler.postDelayed(this, 60_000);
        }
    };

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        getSystemService(NotificationManager.class).createNotificationChannel(
                new NotificationChannel(CHANNEL, "Reproducción", NotificationManager.IMPORTANCE_LOW));
        Notification n = new Notification.Builder(this, CHANNEL)
                .setContentTitle("PaloBox")
                .setContentText("Reproduciendo")
                .setSmallIcon(android.R.drawable.ic_media_play)
                .setOngoing(true)
                .build();
        startForeground(1, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK);
        quiet = 0;
        handler.removeCallbacks(check);
        handler.postDelayed(check, 60_000);
        return START_NOT_STICKY;
    }

    @Override
    public void onDestroy() {
        handler.removeCallbacks(check);
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }
}
