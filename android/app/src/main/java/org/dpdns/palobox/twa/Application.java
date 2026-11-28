package org.dpdns.palobox.twa;

public class Application extends android.app.Application {
    @Override
    public void onCreate() {
        super.onCreate();
        PushService.fetch(this);
    }
}
