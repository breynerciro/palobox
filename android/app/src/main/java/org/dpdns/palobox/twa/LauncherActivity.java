package org.dpdns.palobox.twa;

import android.app.Activity;
import android.app.DownloadManager;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.os.Environment;
import android.view.KeyEvent;
import android.view.ViewGroup;
import android.view.WindowInsets;
import android.webkit.CookieManager;
import android.webkit.URLUtil;
import android.webkit.ValueCallback;
import android.webkit.WebChromeClient;
import android.webkit.WebView;
import android.widget.FrameLayout;
import android.widget.Toast;
import android.window.OnBackInvokedDispatcher;

/**
 * La pantalla de la app: muestra el WebView único del proceso (Web.java). Al cerrarla el WebView sigue vivo, y con
 * él la música, mientras PlaybackService mantenga el proceso.
 */
public class LauncherActivity extends Activity {
    private static final int PICK = 1;
    private WebView web;
    private FrameLayout root;
    private ValueCallback<Uri[]> picked;

    @Override
    protected void onCreate(Bundle state) {
        super.onCreate(state);
        web = Web.get(this);
        Web.attach(this);
        if (web.getParent() instanceof ViewGroup g) g.removeView(web);
        root = new FrameLayout(this);
        root.addView(web);
        // Las barras del sistema no tapan la página: se deja su espacio.
        root.setOnApplyWindowInsetsListener((v, in) -> {
            android.graphics.Insets i = in.getInsets(WindowInsets.Type.systemBars() | WindowInsets.Type.ime());
            v.setPadding(i.left, i.top, i.right, i.bottom);
            return WindowInsets.CONSUMED;
        });
        setContentView(root);
        web.setWebChromeClient(new WebChromeClient() {
            @Override
            public boolean onShowFileChooser(WebView v, ValueCallback<Uri[]> cb, FileChooserParams p) {
                if (picked != null) picked.onReceiveValue(null);
                picked = cb;
                Intent i = p.createIntent();
                if (p.getMode() == FileChooserParams.MODE_OPEN_MULTIPLE) i.putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true);
                try {
                    startActivityForResult(i, PICK);
                } catch (Exception e) {
                    picked = null;
                    return false;
                }
                return true;
            }
        });
        web.setDownloadListener((url, agent, disposition, mime, length) -> {
            String name = URLUtil.guessFileName(url, disposition, mime);
            DownloadManager.Request r = new DownloadManager.Request(Uri.parse(url))
                    .addRequestHeader("Cookie", CookieManager.getInstance().getCookie(url))
                    .setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
                    .setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, name);
            getSystemService(DownloadManager.class).enqueue(r);
            Toast.makeText(this, "Descargando " + name, Toast.LENGTH_SHORT).show();
        });
        if (android.os.Build.VERSION.SDK_INT >= 33) {
            getOnBackInvokedDispatcher().registerOnBackInvokedCallback(OnBackInvokedDispatcher.PRIORITY_DEFAULT, this::back);
        }
        open(getIntent());
    }

    // «Atrás» recorre la app; al llegar al inicio la manda al fondo sin cerrarla: la música sigue.
    private void back() {
        if (web.canGoBack()) web.goBack();
        else moveTaskToBack(true);
    }

    @Override
    @SuppressWarnings("deprecation")
    public void onBackPressed() { back(); }

    // Un enlace de PaloBox (una invitación, un jam) abre aquí, en la página ya cargada.
    private void open(Intent i) {
        Uri u = i == null ? null : i.getData();
        if (u != null && BuildConfig.HOST.equals(u.getHost())) web.loadUrl(u.toString());
    }

    @Override
    protected void onNewIntent(Intent i) {
        super.onNewIntent(i);
        open(i);
    }

    // Con la app a la vista las teclas de medios del control le llegan también al WebView, que pausa por su cuenta
    // mientras el sistema puede volver a darle play: van directo a la sesión, por un solo camino.
    @Override
    public boolean dispatchKeyEvent(KeyEvent e) {
        if (!KeyEvent.isMediaSessionKey(e.getKeyCode())) return super.dispatchKeyEvent(e);
        Player.get(this).session.getController().dispatchMediaButtonEvent(e);
        return true;
    }

    @Override
    protected void onActivityResult(int req, int code, Intent data) {
        super.onActivityResult(req, code, data);
        if (req != PICK || picked == null) return;
        Uri[] got = null;
        if (code == RESULT_OK && data != null) {
            if (data.getClipData() != null) {
                got = new Uri[data.getClipData().getItemCount()];
                for (int k = 0; k < got.length; k++) got[k] = data.getClipData().getItemAt(k).getUri();
            } else if (data.getData() != null) got = new Uri[]{data.getData()};
        }
        picked.onReceiveValue(got);
        picked = null;
    }

    @Override
    protected void onStart() {
        super.onStart();
        Player p = Player.get(this);
        p.visible = true;
        // A la vista se puede arrancar el servicio: si se espera a salir de la app, Android ya no lo deja.
        if (p.sounding) PlaybackService.start(this);
    }

    @Override
    protected void onStop() {
        Player.get(this).visible = false;
        super.onStop();
    }

    @Override
    protected void onDestroy() {
        root.removeView(web);
        web.setWebChromeClient(null);
        web.setDownloadListener(null);
        Web.detach(getApplicationContext());
        super.onDestroy();
    }
}
