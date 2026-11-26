package org.dpdns.palobox.twa;

import android.content.Intent;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.media.AudioManager;
import android.media.MediaMetadata;
import android.media.session.MediaSession;
import android.media.session.PlaybackState;
import android.net.Uri;
import android.os.Bundle;
import android.webkit.CookieManager;
import android.webkit.JavascriptInterface;
import android.webkit.WebView;

import com.google.androidbrowserhelper.trusted.WebViewFallbackActivity;

import org.json.JSONObject;

import java.io.InputStream;
import java.lang.reflect.Field;
import java.net.HttpURLConnection;
import java.net.URL;

/**
 * La app en el televisor. La actividad de la librería pausa el WebView al salir de la app (inicio, otra app,
 * protector de pantalla) y con él la música; esta lo deja corriendo si hay música sonando y arranca
 * PlaybackService para que el sistema no congele el proceso.
 *
 * Un WebView tampoco le cuenta al sistema qué suena (Chrome sí): la página lo pasa por PaloTv.now (player.js) y
 * aquí se publica en una MediaSession. Con eso el inicio del televisor muestra la canción y su carátula, y las
 * teclas de medios del control llegan a la página (window.paloTv).
 */
public class TvActivity extends WebViewFallbackActivity {
    private MediaSession session;
    private WebView web;
    private Field webField;
    private String artUrl = "";
    private Bitmap art;
    private MediaMetadata.Builder meta;

    private final Intent service() { return new Intent(this, PlaybackService.class); }

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        try {
            for (Field f : WebViewFallbackActivity.class.getDeclaredFields()) {
                if (f.getType() == WebView.class) webField = f; // por tipo: el nombre cambia al minificar
            }
            webField.setAccessible(true);
            web = (WebView) webField.get(this);
        } catch (Exception e) {
            webField = null; // sin acceso al WebView: la app funciona, sin música de fondo ni canción en el inicio
        }
        if (web == null) return;
        session = new MediaSession(this, "PaloBox");
        session.setCallback(new MediaSession.Callback() {
            @Override public void onPlay() { js("play", 0); }
            @Override public void onPause() { js("pause", 0); }
            @Override public void onSkipToNext() { js("next", 0); }
            @Override public void onSkipToPrevious() { js("prev", 0); }
            @Override public void onSeekTo(long ms) { js("seek", ms / 1000.0); }
        });
        web.addJavascriptInterface(this, "PaloTv");
    }

    private void js(String cmd, double arg) {
        web.evaluateJavascript("window.paloTv&&window.paloTv('" + cmd + "'," + arg + ")", null);
    }

    /** Lo que suena, en JSON: title, artist, album, art (dirección de la carátula), playing, pos y dur (ms). */
    @JavascriptInterface
    public void now(String json) {
        runOnUiThread(() -> {
            if (session == null) return; // la actividad ya se cerró
            try {
                JSONObject o = new JSONObject(json);
                long dur = o.optLong("dur");
                meta = new MediaMetadata.Builder()
                        .putString(MediaMetadata.METADATA_KEY_TITLE, o.optString("title"))
                        .putString(MediaMetadata.METADATA_KEY_ARTIST, o.optString("artist"))
                        .putString(MediaMetadata.METADATA_KEY_ALBUM, o.optString("album"))
                        .putLong(MediaMetadata.METADATA_KEY_DURATION, dur > 0 ? dur : -1);
                String url = o.optString("art");
                if (!url.equals(artUrl)) {
                    artUrl = url;
                    art = null;
                    loadArt(url);
                }
                publish();
                boolean playing = o.optBoolean("playing");
                session.setPlaybackState(new PlaybackState.Builder()
                        .setActions(PlaybackState.ACTION_PLAY | PlaybackState.ACTION_PAUSE | PlaybackState.ACTION_PLAY_PAUSE
                                | PlaybackState.ACTION_SKIP_TO_NEXT | PlaybackState.ACTION_SKIP_TO_PREVIOUS | PlaybackState.ACTION_SEEK_TO)
                        .setState(playing ? PlaybackState.STATE_PLAYING : PlaybackState.STATE_PAUSED, o.optLong("pos"), playing ? 1f : 0f)
                        .build());
                session.setActive(true);
            } catch (Exception ignored) { // un aviso mal formado no tumba la app
            }
        });
    }

    private void publish() {
        if (art != null) meta.putBitmap(MediaMetadata.METADATA_KEY_ALBUM_ART, art);
        session.setMetadata(meta.build());
    }

    /** Baja la carátula con la sesión de la página; solo del mismo servidor que la página. */
    private void loadArt(String url) {
        String page = web.getUrl();
        String host = Uri.parse(url).getHost();
        if (page == null || host == null || !host.equals(Uri.parse(page).getHost())) return;
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
            } catch (Exception ignored) { // sin carátula: el inicio muestra la canción sin ella
            }
            Bitmap got = b;
            runOnUiThread(() -> {
                if (got == null || session == null || !url.equals(artUrl)) return; // ya suena otra
                art = got;
                publish();
            });
        }).start();
    }

    @Override
    protected void onPause() {
        if (webField == null || !((AudioManager) getSystemService(AUDIO_SERVICE)).isMusicActive()) {
            super.onPause();
            return;
        }
        // super.onPause() es obligatorio y es el que pausa el WebView: se lo escondemos mientras corre.
        boolean hidden = false;
        try {
            webField.set(this, null);
            hidden = true;
        } catch (Exception ignored) { // sin acceso: se pausa como siempre
        }
        try {
            super.onPause();
        } finally {
            try {
                webField.set(this, web);
            } catch (Exception ignored) {
            }
        }
        if (hidden) startForegroundService(service());
    }

    @Override
    protected void onResume() {
        super.onResume();
        stopService(service());
    }

    @Override
    protected void onDestroy() {
        stopService(service()); // «atrás» hasta salir cierra la app: ahí sí se acaba la música
        if (session != null) session.release();
        session = null;
        super.onDestroy();
    }
}
