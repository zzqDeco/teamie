// Package imlimits contains independent IM transport and storage limits.
package imlimits

const AttachmentBytes = 4 * 1024 * 1024
const AttachmentsPerMessage = 8
const PendingAttachmentsPerSender = 32
const AttachmentRequestBytes = AttachmentBytes + 64*1024
