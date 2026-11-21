package com.daybreak.nvr;

import android.content.Context;
import android.content.SharedPreferences;

import java.util.Map;

/**
 * Saved preferences: just the two toggles. The viewer handles
 * sign-in itself, so no server address or credentials are stored
 * here anymore. Any address / credential / mode entries saved by
 * an earlier version are wiped on first use (matched by suffix
 * and key name, so no old key strings live in this file).
 */
public class NvrPrefs {
    private static final String PREFS = "nvr_prefs";
    private static final String K_BIOMETRIC = "biometric";
    private static final String K_KEEP_SCREEN_ON = "keep_screen_on";
    private static final String K_CACHE_CLEARED_VERSION = "cache_cleared_version";

    private final SharedPreferences prefs;

    public NvrPrefs(Context context) {
        prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
        clearLegacyEntries();
    }

    private void clearLegacyEntries() {
        Map<String, ?> all = prefs.getAll();
        if (all.isEmpty()) return;
        SharedPreferences.Editor ed = prefs.edit();
        boolean changed = false;
        for (String key : all.keySet()) {
            if (key.endsWith("_enc") || "mode".equals(key) || "configured".equals(key)) {
                ed.remove(key);
                changed = true;
            }
        }
        if (changed) ed.apply();
    }

    public boolean isBiometricEnabled() {
        return prefs.getBoolean(K_BIOMETRIC, false);
    }

    public boolean isKeepScreenOn() {
        return prefs.getBoolean(K_KEEP_SCREEN_ON, true);
    }

    public void setBiometricEnabled(boolean enabled) {
        prefs.edit().putBoolean(K_BIOMETRIC, enabled).apply();
    }

    public void setKeepScreenOn(boolean enabled) {
        prefs.edit().putBoolean(K_KEEP_SCREEN_ON, enabled).apply();
    }

    /** Version code the WebView cache was last cleared for (0 = never). */
    public int getCacheClearedVersion() {
        return prefs.getInt(K_CACHE_CLEARED_VERSION, 0);
    }

    public void setCacheClearedVersion(int versionCode) {
        prefs.edit().putInt(K_CACHE_CLEARED_VERSION, versionCode).apply();
    }
}
