import java.io.FileInputStream
import java.util.Properties

plugins {
    id("com.android.application")
    // The Flutter Gradle Plugin must be applied after the Android and Kotlin Gradle plugins.
    id("dev.flutter.flutter-gradle-plugin")
}

android {
    namespace = "eu.tjena.tjena_bridge"
    compileSdk = flutter.compileSdkVersion
    ndkVersion = flutter.ndkVersion

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    defaultConfig {
        applicationId = "eu.tjena.bridge"
        // Android 10+: exec only from nativeLibraryDir; the bridge binary is arm64-only.
        minSdk = 29
        targetSdk = flutter.targetSdkVersion
        versionCode = flutter.versionCode
        versionName = flutter.versionName
        ndk { abiFilters += listOf("arm64-v8a") }
    }

    // The bridge and relay executables ship as lib*.so in src/main/jniLibs/arm64-v8a
    // (filled by personal-bridge/android-binary/build.sh). They must be extracted to
    // nativeLibraryDir, the only app location Android lets us exec from.
    packaging {
        jniLibs {
            useLegacyPackaging = true
            keepDebugSymbols += listOf("**/libmautrixwhatsapp.so", "**/librelay.so")
        }
    }

    // Release signing: bridge_app/android/key.properties (git-ignored), or the
    // file named by TJENA_KEY_PROPERTIES. It must always be the same key, or
    // installed apps (and Obtainium) can't update. Without it: debug key.
    val keystoreProperties = Properties()
    val keystorePropertiesFile = System.getenv("TJENA_KEY_PROPERTIES")?.let { file(it) }
        ?: rootProject.file("key.properties")
    if (keystorePropertiesFile.exists()) {
        keystoreProperties.load(FileInputStream(keystorePropertiesFile))
    }
    signingConfigs {
        create("release") {
            if (keystorePropertiesFile.exists()) {
                keyAlias = keystoreProperties["keyAlias"] as String
                keyPassword = keystoreProperties["keyPassword"] as String
                storeFile = keystoreProperties["storeFile"]?.let { file(it) }
                storePassword = keystoreProperties["storePassword"] as String
            }
        }
    }

    buildTypes {
        release {
            signingConfig = if (keystorePropertiesFile.exists()) {
                signingConfigs.getByName("release")
            } else {
                signingConfigs.getByName("debug")
            }
        }
    }
}

kotlin {
    compilerOptions {
        jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
    }
}

flutter {
    source = "../.."
}
