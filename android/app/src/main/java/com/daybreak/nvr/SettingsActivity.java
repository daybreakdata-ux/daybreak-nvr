package com.daybreak.nvr;

import android.app.Activity;
import android.os.Build;
import android.os.Bundle;
import android.view.View;
import android.view.ViewGroup;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.Switch;
import android.widget.TextView;
import android.widget.Toast;

/**
 * Settings: just the two toggles. The viewer is remote-only and
 * handles sign-in itself, so there is no server or mode setup here.
 * Labels are plain and functional; the app name appears only as
 * the launcher label.
 */
public class SettingsActivity extends Activity {

    private static final int BG = 0xFF001119;
    private static final int CARD = 0xFF050607;
    private static final int ACCENT = 0xFF168573;
    private static final int TEXT = 0xFFE9F1F3;
    private static final int DIM = 0xFF8BA3AD;

    private NvrPrefs prefs;
    private Switch biometricSwitch;
    private Switch keepScreenSwitch;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        prefs = new NvrPrefs(this);
        getWindow().setStatusBarColor(BG);
        getWindow().setNavigationBarColor(BG);
        buildUi();
    }

    private void buildUi() {
        ScrollView scroll = new ScrollView(this);
        scroll.setBackgroundColor(BG);
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(dp(20), dp(16), dp(20), dp(24));
        scroll.addView(root, new ViewGroup.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT));

        TextView title = new TextView(this);
        title.setText("Settings");
        title.setTextColor(TEXT);
        title.setTextSize(22f);
        root.addView(title);

        root.addView(sectionHeader("Security"));
        biometricSwitch = new Switch(this);
        biometricSwitch.setText("Require fingerprint or face unlock");
        biometricSwitch.setTextColor(TEXT);
        biometricSwitch.setChecked(prefs.isBiometricEnabled());
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.P) {
            biometricSwitch.setEnabled(false);
            biometricSwitch.setText("Require fingerprint or face unlock (needs Android 9+)");
        }
        root.addView(biometricSwitch);

        root.addView(sectionHeader("Viewing"));
        keepScreenSwitch = new Switch(this);
        keepScreenSwitch.setText("Keep screen on while viewing");
        keepScreenSwitch.setTextColor(TEXT);
        keepScreenSwitch.setChecked(prefs.isKeepScreenOn());
        root.addView(keepScreenSwitch);

        Button save = new Button(this);
        save.setText("Save");
        LinearLayout.LayoutParams sp =
                new LinearLayout.LayoutParams(
                        ViewGroup.LayoutParams.MATCH_PARENT, dp(52));
        sp.setMargins(0, dp(24), 0, 0);
        save.setOnClickListener(new android.view.View.OnClickListener() {
            @Override
            public void onClick(View v) {
                save();
            }
        });
        root.addView(save, sp);

        setContentView(scroll);
    }

    private TextView sectionHeader(String text) {
        TextView h = new TextView(this);
        h.setText(text);
        h.setTextColor(ACCENT);
        h.setTextSize(13f);
        h.setPadding(0, dp(22), 0, dp(6));
        return h;
    }

    private void save() {
        prefs.setBiometricEnabled(biometricSwitch.isChecked());
        prefs.setKeepScreenOn(keepScreenSwitch.isChecked());
        Toast.makeText(this, "Saved.", Toast.LENGTH_SHORT).show();
        setResult(RESULT_OK);
        finish();
    }

    private int dp(int value) {
        return (int) (value * getResources().getDisplayMetrics().density);
    }
}
