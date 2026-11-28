package org.dpdns.palobox.twa;

import android.app.NotificationManager;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.net.wifi.WifiManager;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;
import android.os.PowerManager;

/**
 * Servicio en primer plano con la notificación de lo que suena. Sin él, con la pantalla apagada, el sistema congela el
 * proceso (y con él el WebView y la música) y Doze le corta la red. No reproduce nada: el audio es el del WebView.
 *
 * Sigue en pie un rato después de pausar (QUIET): así la pantalla bloqueada conserva los controles y, si desde otro
 * dispositivo devuelven la música, la página sigue conectada a la sala y la oye. Pasado ese rato se suelta.
 */
public class PlaybackService extends Service {
    private static final long QUIET = 10 * 60_000;
    private static PlaybackService one; // el servicio corriendo, o null
    private final Handler handler = new Handler(Looper.getMainLooper());
    private WifiManager.WifiLock wifi;
    private PowerManager.WakeLock cpu;

    /** Lo arranca si no está. Desde segundo plano Android puede no dejar (salvo con un push): se ignora. */
    static void start(Context c) {
        if (one != null) return;
        try {
            c.startForegroundService(new Intent(c, PlaybackService.class));
        } catch (Exception ignored) { // ForegroundServiceStartNotAllowedException: la app no está a la vista
        }
    }

    /** Pone al día la notificación y los candados con lo que suena. */
    static void refresh(Context c) {
        if (one == null) return;
        Player p = Player.get(c);
        c.getSystemService(NotificationManager.class).notify(Player.ID, p.notification());
        one.hold(p.sounding);
    }

    private final Runnable check = new Runnable() {
        @Override
        public void run() {
            Player p = Player.get(PlaybackService.this);
            if (!p.sounding && !p.visible && System.currentTimeMillis() - p.silentSince > QUIET) {
                stopForeground(STOP_FOREGROUND_REMOVE);
                stopSelf();
                return;
            }
            handler.postDelayed(this, 60_000);
        }
    };

    @Override
    public void onCreate() {
        super.onCreate();
        wifi = getSystemService(WifiManager.class).createWifiLock(WifiManager.WIFI_MODE_FULL_LOW_LATENCY, "palobox:musica");
        wifi.setReferenceCounted(false);
        cpu = getSystemService(PowerManager.class).newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "palobox:musica");
        cpu.setReferenceCounted(false);
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        Player p = Player.get(this);
        startForeground(Player.ID, p.notification(), ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK);
        one = this;
        String cmd = intent == null ? null : intent.getAction();
        if (cmd != null) Web.command(cmd, 0); // un botón de la notificación
        hold(p.sounding);
        handler.removeCallbacks(check);
        handler.postDelayed(check, 60_000);
        return START_NOT_STICKY;
    }

    // Con la pantalla apagada el Wi-Fi se duerme y el flujo de la sala se corta: mientras suena, se mantiene despierto.
    private void hold(boolean playing) {
        if (playing) {
            wifi.acquire();
            cpu.acquire(); // shortcut: sin límite de tiempo; se suelta al pausar o al cerrar el servicio
        } else {
            if (wifi.isHeld()) wifi.release();
            if (cpu.isHeld()) cpu.release();
        }
    }

    // Cerrar la app desde las recientes la cierra de verdad: se pausa la sala (para que no siga «sonando» en los
    // demás dispositivos) y, con un momento para que esa pausa llegue al servidor, se termina el proceso.
    @Override
    public void onTaskRemoved(Intent root) {
        Web.command("pause", 0);
        handler.removeCallbacks(check);
        handler.postDelayed(() -> {
            stopForeground(STOP_FOREGROUND_REMOVE);
            stopSelf();
            android.os.Process.killProcess(android.os.Process.myPid());
        }, 1500);
    }

    @Override
    public void onDestroy() {
        one = null;
        handler.removeCallbacks(check);
        hold(false);
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) {
        return null;
    }
}
