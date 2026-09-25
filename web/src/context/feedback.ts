import { createContext, useContext, type ReactNode } from 'react'

export type ToastKind = 'info' | 'error'
export type ShowToast = (text: string, kind?: ToastKind) => void

export const ToastContext = createContext<ShowToast>(() => {})

/** Shows a short message in the corner. */
export const useToast = () => useContext(ToastContext)

export interface ConfirmOptions {
  title: string
  body: ReactNode
  confirmLabel: string
  danger?: boolean
}

export type Confirm = (options: ConfirmOptions) => Promise<boolean>

export const ConfirmContext = createContext<Confirm>(() => Promise.resolve(false))

/** Asks the operator to confirm; resolves to false on cancel. */
export const useConfirm = () => useContext(ConfirmContext)
