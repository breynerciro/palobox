package org.dpdns.palobox.twa;

import android.content.Context;
import android.os.Handler;
import android.os.Looper;
import android.webkit.WebView;

import com.google.firebase.messaging.FirebaseMessaging;
import com.google.firebase.messaging.FirebaseMessagingService;
import com.google.firebase.messaging.RemoteMessage;

/**
 * Avisos de Firebase. «play»: desde otro dispositivo le pasaron la música a este, que puede estar dormido (pantalla
 * bloqueada, app congelada, o el proceso cerrado por el sistema). Un aviso de prioridad alta deja arrancar el servicio
 * de reproducción desde segundo plano; la página se despierta (o arranca) y ella sola se conecta a la sala y suena.
 */
public class PushService extends FirebaseMessagingService {
    private static final String PREFS = "push";

    static String token(Context c) {
        return c.getSharedPreferences(PREFS, MODE_PRIVATE).getString("token", "");
    }

    private static void save(Context c, String t) {
        c.getSharedPreferences(PREFS, MODE_PRIVATE).edit().putString("token", t).apply();
    }

    /** Pide el token al abrir la app: el primero tarda un momento en llegar. */
    static void fetch(Context c) {
        try {
            FirebaseMessaging.getInstance().getToken().addOnSuccessListener(t -> save(c, t));
        } catch (Exception ignored) { // sin Google Play Services: la app funciona, solo que no se despierta sola
        }
    }

    @Override
    public void onNewToken(String t) {
        save(this, t);
    }

    @Override
    public void onMessageReceived(RemoteMessage m) {
        if (!"play".equals(m.getData().get("op"))) return;
        PlaybackService.start(this);
        new Handler(Looper.getMainLooper()).post(() -> {
            WebView w = Web.peek();
            if (w == null) Web.get(this); // el proceso se había cerrado: la página arranca y se conecta sola
            else w.evaluateJavascript("window.paloWake&&window.paloWake()", null);
        });
    }
}
