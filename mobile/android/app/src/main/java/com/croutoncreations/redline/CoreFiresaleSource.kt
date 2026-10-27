package com.croutoncreations.redline

import core.Core

class CoreFiresaleSource(private val holder: CoreClientHolder) : FiresaleSource {
    override fun list(): String = holder.client().listFiresales()
    override fun start(account: String, until: String) = holder.client().startFiresale(account, until)
    override fun stop(account: String) = holder.client().stopFiresale(account)
    override fun isNotFound(error: Throwable): Boolean = error is Exception && Core.isNotFound(error)
}
