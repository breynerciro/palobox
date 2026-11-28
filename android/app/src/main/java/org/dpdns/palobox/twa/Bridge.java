package org.dpdns.palobox.twa;

import android.content.Context;
import android.os.Handler;
import android.os.Looper;
import android.webkit.JavascriptInterface;

/** window.PaloTv: lo que la página le cuenta a la app. Llega en el hilo del JS; se atiende en el principal. */
final class Bridge {
    private final Context app;
    private final Handler main = new Handler(Looper.getMainLooper());

    Bridge(Context app) { this.app = app; }

    /** Lo que suena, en JSON: title, artist, album, art (dirección de la carátula), playing, pos y dur (ms). */
    @JavascriptInterface
    public void now(String json) {
        main.post(() -> Player.get(app).update(json));
    }

    /** Token de push de este teléfono; vacío si Firebase aún no lo entrega (la página lo vuelve a pedir al reconectar). */
    @JavascriptInterface
    public String token() { return PushService.token(app); }

    /** Nombre del equipo para la lista de dispositivos: el que le puso su dueño en Ajustes (el mismo que muestra
     *  Spotify, como «CiroTV»), o el modelo. */
    @JavascriptInterface
    public String name() {
        String n = android.provider.Settings.Global.getString(app.getContentResolver(), "device_name");
        return n == null || n.isBlank() ? android.os.Build.MODEL : n.trim();
    }

    @JavascriptInterface
    public int version() { return BuildConfig.VERSION_CODE; }
}
