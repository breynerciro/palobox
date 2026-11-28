package org.dpdns.palobox.twa;

import android.app.UiModeManager;
import android.content.Context;
import android.content.Intent;
import android.content.MutableContextWrapper;
import android.content.res.Configuration;
import android.net.Uri;
import android.webkit.CookieManager;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;

/**
 * El WebView único del proceso, con la app web adentro. No es de la pantalla sino del proceso: así sigue corriendo
 * (y sonando) con la pantalla cerrada mientras PlaybackService mantenga vivo el proceso, y lo puede crear quien lo
 * necesite primero. La pantalla (LauncherActivity) solo lo muestra; nunca lo pausa.
 */
final class Web {
    private static WebView view;
    private static MutableContextWrapper ctx; // se pasa a la pantalla mientras está abierta: diálogos y selectores la necesitan

    static WebView get(Context c) {
        if (view != null) return view;
        ctx = new MutableContextWrapper(c.getApplicationContext());
        // Consola de la página desde el computador (chrome://inspect), solo si el celular tiene la depuración USB activa.
        WebView.setWebContentsDebuggingEnabled(android.provider.Settings.Global.getInt(c.getContentResolver(), android.provider.Settings.Global.ADB_ENABLED, 0) == 1);
        WebView w = new WebView(ctx);
        WebSettings s = w.getSettings();
        s.setJavaScriptEnabled(true);
        s.setDomStorageEnabled(true);
        s.setMediaPlaybackRequiresUserGesture(false); // «suena aquí» desde otro dispositivo llega sin un toque
        s.setUserAgentString(s.getUserAgentString() + " PaloBoxApp/" + BuildConfig.VERSION_CODE);
        CookieManager.getInstance().setAcceptCookie(true);
        w.setBackgroundColor(0xFF090D16);
        w.setWebViewClient(new WebViewClient() {
            // Lo que no es PaloBox (un enlace de Spotify, una ayuda) se abre afuera, en el navegador.
            @Override
            public boolean shouldOverrideUrlLoading(WebView v, WebResourceRequest r) {
                Uri u = r.getUrl();
                if (BuildConfig.HOST.equals(u.getHost())) return false;
                try {
                    ctx.startActivity(new Intent(Intent.ACTION_VIEW, u).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
                } catch (Exception ignored) { // nada lo abre
                }
                return true;
            }
        });
        w.addJavascriptInterface(new Bridge(c.getApplicationContext()), "PaloTv");
        w.loadUrl(home(c));
        view = w;
        return w;
    }

    static WebView peek() { return view; }

    static void attach(Context activity) { if (ctx != null) ctx.setBaseContext(activity); }

    static void detach(Context app) { if (ctx != null) ctx.setBaseContext(app); }

    /** En el televisor la app abre con ?tv (web/tv.js); en el teléfono, en el inicio de siempre. */
    static String home(Context c) {
        boolean tv = c.getSystemService(UiModeManager.class).getCurrentModeType() == Configuration.UI_MODE_TYPE_TELEVISION;
        return "https://" + BuildConfig.HOST + (tv ? "/?tv" : "/");
    }

    /** Manda un comando del sistema (notificación, pantalla bloqueada, control remoto) a la página: web/player.js. */
    static void command(String cmd, double arg) {
        if (view != null) view.evaluateJavascript("window.paloTv&&window.paloTv('" + cmd + "'," + arg + ")", null);
    }
}
