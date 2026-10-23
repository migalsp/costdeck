import { leavePage, stayOnPage, usePendingLeave } from '../lib/router'
import { Button, Modal } from './ui'

// LeaveDialog asks before a page with unsaved input is left (see useLeaveGuard). Staying
// is the default: it has the focus, and Escape or the close button choose it too.
export default function LeaveDialog() {
  const unsaved = usePendingLeave()
  if (!unsaved) return null
  return (
    <Modal title="Leave without saving?" size="sm" onClose={stayOnPage} dismissOnBackdrop={false}
      footer={<>
        <Button onClick={stayOnPage} autoFocus>Stay</Button>
        <Button variant="danger" onClick={leavePage}>Leave without saving</Button>
      </>}>
      <p className="text-sm text-slate-600">The changes in {unsaved} are not saved yet and will be lost.</p>
    </Modal>
  )
}
