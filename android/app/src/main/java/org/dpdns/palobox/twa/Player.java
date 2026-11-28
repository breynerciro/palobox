package org.dpdns.palobox.twa;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.graphics.drawable.Icon;
import android.media.MediaMetadata;
import android.media.session.MediaSession;
import android.media.session.PlaybackState;
import android.net.Uri;
import android.os.Handler;
import android.os.Looper;
import android.webkit.CookieManager;

import org.json.JSONObject;

import java.io.InputStream;
import java.net.HttpURLConnection;
import java.net.URL;

/**
 * Lo que suena, para el sistema: la MediaSession (pantalla bloqueada, Bluetooth, teclas de medios, inicio del
 * televisor) y la notificación con los controles. El audio lo produce la página en el WebView (Web.java); ella cuenta
 * qué suena por PaloTv.now y aquí solo se refleja. Los botones vuelven a la página como comandos (Web.command).
 */
final class Player {
    static final String CHANNEL = "playback";
    static final int ID = 1;
    private static Player one;

    final Context app;
    final MediaSession session;
    private final Handler main = new Handler(Looper.getMainLooper());
    boolean playing; // la sala suena (aquí o en otro dispositivo: los controles sirven de control remoto)
    boolean sounding; // suena en este dispositivo: solo entonces hace falta el servicio y mantener despierta la red
    boolean visible; // la pantalla de la app está a la vista
    long silentSince = System.currentTimeMillis();
    private String title = "PaloBox", artist = "", artUrl = "";
    private Bitmap art;
    private MediaMetadata.Builder meta = new MediaMetadata.Builder();

    static Player get(Context c) {
        if (one == null) one = new Player(c.getApplicationContext());
        return one;
    }

    private Player(Context app) {
        this.app = app;
        app.getSystemService(NotificationManager.class).createNotificationChannel(
                new NotificationChannel(CHANNEL, "Reproducción", NotificationManager.IMPORTANCE_LOW));
        session = new MediaSession(app, "PaloBox");
        session.setCallback(new MediaSession.Callback() {
            @Override public void onPlay() { Web.command("play", 0); }
            @Override public void onPause() { Web.command("pause", 0); }
            @Override public void onSkipToNext() { Web.command("next", 0); }
            @Override public void onSkipToPrevious() { Web.command("prev", 0); }
            @Override public void onSeekTo(long ms) { Web.command("seek", ms / 1000.0); }
        });
        session.setSessionActivity(open());
    }

    void update(String json) {
        try {
            JSONObject o = new JSONObject(json);
            long dur = o.optLong("dur");
            title = o.optString("title", "PaloBox");
            artist = o.optString("artist");
            meta = new MediaMetadata.Builder()
                    .putString(MediaMetadata.METADATA_KEY_TITLE, title)
                    .putString(MediaMetadata.METADATA_KEY_ARTIST, artist)
                    .putString(MediaMetadata.METADATA_KEY_ALBUM, o.optString("album"))
                    .putLong(MediaMetadata.METADATA_KEY_DURATION, dur > 0 ? dur : -1);
            String url = o.optString("art");
            if (!url.equals(artUrl)) {
                artUrl = url;
                art = null;
                loadArt(url);
            }
            if (art != null) meta.putBitmap(MediaMetadata.METADATA_KEY_ALBUM_ART, art);
            session.setMetadata(meta.build());
            boolean now = o.optBoolean("playing");
            session.setPlaybackState(new PlaybackState.Builder()
                    .setActions(PlaybackState.ACTION_PLAY | PlaybackState.ACTION_PAUSE | PlaybackState.ACTION_PLAY_PAUSE
                            | PlaybackState.ACTION_SKIP_TO_NEXT | PlaybackState.ACTION_SKIP_TO_PREVIOUS | PlaybackState.ACTION_SEEK_TO)
                    .setState(now ? PlaybackState.STATE_PLAYING : PlaybackState.STATE_PAUSED, o.optLong("pos"), now ? 1f : 0f)
                    .build());
            // Android avisa a quien muestra lo que suena (el inicio del televisor) cuando una sesión se activa, no
            // cuando empieza a sonar: al arrancar se vuelve a activar para que pase a mostrar esta y no la de otra app.
            if (now && !playing) session.setActive(false);
            session.setActive(true);
            boolean here = now && o.optBoolean("here", true);
            if (!here && sounding) silentSince = System.currentTimeMillis();
            playing = now;
            sounding = here;
            if (sounding) PlaybackService.start(app);
            PlaybackService.refresh(app);
        } catch (Exception ignored) { // un aviso mal formado no tumba la app
        }
    }

    Notification notification() {
        Notification.Builder b = new Notification.Builder(app, CHANNEL)
                .setSmallIcon(android.R.drawable.ic_media_play)
                .setContentTitle(title)
                .setContentText(artist)
                .setLargeIcon(art)
                .setContentIntent(open())
                .setVisibility(Notification.VISIBILITY_PUBLIC)
                .setOngoing(playing)
                .addAction(action(android.R.drawable.ic_media_previous, "Anterior", "prev"))
                .addAction(playing ? action(android.R.drawable.ic_media_pause, "Pausar", "pause")
                        : action(android.R.drawable.ic_media_play, "Reproducir", "play"))
                .addAction(action(android.R.drawable.ic_media_next, "Siguiente", "next"))
                .setStyle(new Notification.MediaStyle().setMediaSession(session.getSessionToken()).setShowActionsInCompactView(0, 1, 2));
        return b.build();
    }

    private Notification.Action action(int icon, String label, String cmd) {
        Intent i = new Intent(app, PlaybackService.class).setAction(cmd);
        PendingIntent p = PendingIntent.getService(app, cmd.hashCode(), i, PendingIntent.FLAG_IMMUTABLE);
        return new Notification.Action.Builder(Icon.createWithResource(app, icon), label, p).build();
    }

    private PendingIntent open() {
        Intent i = new Intent(app, LauncherActivity.class).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP);
        return PendingIntent.getActivity(app, 0, i, PendingIntent.FLAG_IMMUTABLE);
    }

    /** Baja la carátula con la sesión de la página; solo del mismo servidor que la página. */
    private void loadArt(String url) {
        Uri u = Uri.parse(url);
        if (!BuildConfig.HOST.equals(u.getHost())) return;
        String cookie = CookieManager.getInstance().getCookie(url);
        new Thread(() -> {
            Bitmap b = null;
            try {
                HttpURLConnection c = (HttpURLConnection) new URL(url).openConnection();
                if (cookie != null) c.setRequestProperty("Cookie", cookie);
                c.setConnectTimeout(10_000);
                c.setReadTimeout(10_000);
                try (InputStream in = c.getInputStream()) {
                    b = BitmapFactory.decodeStream(in);
                } finally {
                    c.disconnect();
                }
            } catch (Exception ignored) { // sin carátula: se muestra la canción sin ella
            }
            Bitmap got = b;
            main.post(() -> {
                if (got == null || !url.equals(artUrl)) return; // ya suena otra
                art = got;
                session.setMetadata(meta.putBitmap(MediaMetadata.METADATA_KEY_ALBUM_ART, got).build());
                PlaybackService.refresh(app);
            });
        }).start();
    }
}
