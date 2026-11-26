package org.dpdns.palobox.twa;

import android.content.Intent;
import android.media.AudioManager;
import android.webkit.WebView;

import com.google.androidbrowserhelper.trusted.WebViewFallbackActivity;

import java.lang.reflect.Field;

/**
 * La app en el televisor. La actividad de la librería pausa el WebView al salir de la app (inicio, otra app,
 * protector de pantalla) y con él la música; esta lo deja corriendo si hay música sonando y arranca
 * PlaybackService para que el sistema no congele el proceso.
 */
public class TvActivity extends WebViewFallbackActivity {
    private final Intent service() { return new Intent(this, PlaybackService.class); }

    @Override
    protected void onPause() {
        if (!((AudioManager) getSystemService(AUDIO_SERVICE)).isMusicActive()) {
            super.onPause();
            return;
        }
        // super.onPause() es obligatorio y es el que pausa el WebView: se lo escondemos mientras corre.
        Field web = null;
        Object view = null;
        try {
            for (Field f : WebViewFallbackActivity.class.getDeclaredFields()) {
                if (f.getType() == WebView.class) web = f; // por tipo: el nombre cambia al minificar
            }
            web.setAccessible(true);
            view = web.get(this);
            web.set(this, null);
        } catch (Exception e) {
            web = null; // sin acceso: se pausa como siempre
        }
        try {
            super.onPause();
        } finally {
            try {
                if (web != null) web.set(this, view);
            } catch (Exception ignored) {
            }
        }
        if (web != null) startForegroundService(service());
    }

    @Override
    protected void onResume() {
        super.onResume();
        stopService(service());
    }

    @Override
    protected void onDestroy() {
        stopService(service()); // «atrás» hasta salir cierra la app: ahí sí se acaba la música
        super.onDestroy();
    }
}
