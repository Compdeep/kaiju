/**
 * desc: Frontend upload pipeline — pushes a "pending" placeholder onto
 * the active session's attachments list, POSTs the file to
 * /api/v1/sessions/<sid>/uploads as multipart, then replaces the
 * placeholder with the server's Result (or marks it as errored).
 *
 * The backend pipeline is synchronous (validate → write → extract →
 * optional LLM summary → memory entry), so the chip stays in
 * "uploading…" state for the full duration. No polling, no async
 * status reconciliation needed.
 */

import { useSessionsStore } from '../stores/sessions'
import * as chat from './chat'
import api from '../api/client'

/**
 * Upload one file, drive the chip through pending → ready/error.
 * @param {File} file
 * @returns {Promise<void>}
 */
export async function uploadOne(file) {
  const sessions = useSessionsStore()

  // Ensure a session exists before uploading. The session id is part of
  // the upload's destination directory and memory tag.
  if (!sessions.sessionId) {
    await chat.createSession()
  }

  const placeholder = {
    filename: file.name,
    size: file.size,
    type: file.type || '',
    pending: true,
  }
  sessions.attachments.push(placeholder)

  const form = new FormData()
  form.append('file', file)

  try {
    const res = await api.postRaw(
      `/api/v1/sessions/${sessions.sessionId}/uploads`,
      form
    )
    // Replace the placeholder with the real result, preserving order.
    const idx = sessions.attachments.indexOf(placeholder)
    if (idx >= 0) {
      sessions.attachments.splice(idx, 1, { ...res, pending: false })
    }
  } catch (err) {
    const idx = sessions.attachments.indexOf(placeholder)
    if (idx >= 0) {
      sessions.attachments.splice(idx, 1, {
        ...placeholder,
        pending: false,
        error: err.message || 'upload failed',
      })
    }
  }
}

/**
 * desc: Upload several files concurrently. Each runs through uploadOne
 * independently so one failure doesn't block the others.
 * @param {File[]} files
 */
export async function uploadMany(files) {
  await Promise.all(files.map(uploadOne))
}

/**
 * desc: Remove an attachment from the active session — both server-side
 * (delete file + sidecars + memory entry) and from the local list.
 * @param {Object} att - the attachment object to remove
 */
export async function removeAttachment(att) {
  const sessions = useSessionsStore()
  // Local removal first so the UI feels instant.
  const idx = sessions.attachments.indexOf(att)
  if (idx >= 0) sessions.attachments.splice(idx, 1)
  // Skip server call for upload errors that never produced a file.
  if (!att.path || !sessions.sessionId) return
  try {
    await api.del(`/api/v1/sessions/${sessions.sessionId}/uploads/${encodeURIComponent(att.filename)}`)
  } catch (err) {
    console.error('[uploads] delete failed:', err)
  }
}

/**
 * desc: Restore the chip strip when a session is reloaded by reading
 * the session's existing uploads from the server.
 */
export async function loadAttachments() {
  const sessions = useSessionsStore()
  if (!sessions.sessionId) return
  try {
    const list = await api.get(`/api/v1/sessions/${sessions.sessionId}/uploads`)
    if (Array.isArray(list)) sessions.attachments = list.map(a => ({ ...a, pending: false }))
  } catch (err) {
    console.error('[uploads] load failed:', err)
  }
}
