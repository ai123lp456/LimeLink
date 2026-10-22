package com.limelink.app

import android.app.Application

class AndroidApp : Application() {
    override fun onCreate() {
        super.onCreate()
        instance = this
    }

    companion object {
        var instance: AndroidApp? = null
            private set
    }
}