plugins {
    id("persona.jvm.library")
}

dependencies {
    api(project(":core:contracts"))
    api(libs.okhttp)
    // The SSH tunnel to the hub (docs/operations/network.md "Connecting to hub through SSH"); Tink signs and agrees keys for it.
    implementation(libs.jsch)
    implementation(libs.tink.android)
    testImplementation(libs.coroutines.test)
    testImplementation("com.squareup.okhttp3:mockwebserver:${libs.versions.okhttp.get()}")
    testImplementation("com.squareup.okhttp3:okhttp-tls:${libs.versions.okhttp.get()}")
    // A real SSH server for the tunnel's tests; eddsa lets it check this device's Ed25519 key.
    testImplementation(libs.sshd.core)
    testImplementation(libs.eddsa)
}
