import { useCallback, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { ConfirmContext, ToastContext, type ConfirmOptions, type ToastKind } from '../context/feedback'
import { Modal } from './Modal'

interface Toast {
  id: number
  text: string
  kind: ToastKind
}

interface PendingConfirm extends ConfirmOptions {
  resolve: (ok: boolean) => void
}

/** Toasts and confirmation dialogs for the whole app. */
export function FeedbackProvider({ children }: { children: ReactNode }) {
  const { t } = useTranslation()
  const [toasts, setToasts] = useState<Toast[]>([])
  const [pending, setPending] = useState<PendingConfirm | null>(null)
  const nextId = useRef(0)

  const toast = useCallback((text: string, kind: ToastKind = 'info') => {
    const id = ++nextId.current
    setToasts((list) => [...list, { id, text, kind }])
    window.setTimeout(() => setToasts((list) => list.filter((x) => x.id !== id)), kind === 'error' ? 8000 : 4000)
  }, [])

  const confirm = useCallback(
    (options: ConfirmOptions) => new Promise<boolean>((resolve) => setPending({ ...options, resolve })),
    [],
  )

  const finish = (ok: boolean) => {
    pending?.resolve(ok)
    setPending(null)
  }

  return (
    <ToastContext.Provider value={toast}>
      <ConfirmContext.Provider value={confirm}>
        {children}
        {pending && (
          <Modal
            title={pending.title}
            onClose={() => finish(false)}
            footer={
              <>
                <button type="button" onClick={() => finish(false)}>
                  {t('common.cancel')}
                </button>
                <button
                  type="button"
                  className={pending.danger ? 'primary danger' : 'primary'}
                  onClick={() => finish(true)}
                >
                  {pending.confirmLabel}
                </button>
              </>
            }
          >
            <div className="confirm-body">{pending.body}</div>
          </Modal>
        )}
        <div className="toasts" role="status" aria-live="polite">
          {toasts.map((x) => (
            <div key={x.id} className={x.kind === 'error' ? 'toast error' : 'toast'}>
              {x.text}
            </div>
          ))}
        </div>
      </ConfirmContext.Provider>
    </ToastContext.Provider>
  )
}
