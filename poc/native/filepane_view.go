package main

import (
	"context"
	"fmt"

	"github.com/egoist/mygo/ui"
)

// The view composes named sections; operations and their authorization guards
// live in filepane.go. Dialog ownership is captured on the UI thread.
func (p *filePane) view(c *ui.Context, ctx context.Context, ja, demo, enabled bool, client *exchangeClient, processID, peerID string, transfers []exchangeTransfer) {
	p.drain()
	t := fileText(translator(ja).text)
	if p.takeAnnouncement() {
		c.Announce(t("Action status changed. Review the status below.", "操作状況が変わりました。下の結果を確認してください。"))
	}
	a := fileActions{ctx: ctx, wake: c.Services().Invalidate, demo: demo, enabled: enabled, client: client, process: processID, peer: peerID, transfers: transfers}
	a.pick = nativeFilePicker(t)
	ui.Column(c).FillWidth().Gap(c.Theme().Space(1.75)).Children(func() {
		p.filePickerView(c, a, t)
		if p.review.PreparationID != "" {
			p.outgoingReviewView(c, a, t)
		}
		if p.uncertain && p.preparation != "" && (demo || client != nil) {
			if ui.Button(c, t("Check staging status", "準備状態を確認")).Disabled(!p.canCheckStatus(a)).Clicked() {
				p.checkStatus(a)
			}
		}
		if p.incoming != nil {
			p.incomingReviewView(c, a, t)
		}
		p.fileStatusView(c, a, t)
		p.dispatchReceiptView(c, a, t)
		p.transferHistoryView(c, a, t, ja)
	})
}
func fileDetail(c *ui.Context, text string) { ui.Text(c, text).FontSize(nativeStyles(c).caption) }
func (p *filePane) filePickerView(c *ui.Context, a fileActions, t fileText) {
	ui.Text(c, t("Files", "ファイル")).Bold()
	if !a.enabled {
		fileDetail(c, t("Read only. File actions are disabled.", "読み取り専用です。ファイル操作は無効です。"))
		return
	}
	if ui.Button(c, t("Choose files…", "ファイルを選択…")).Disabled(!p.canChoose(a)).Clicked() {
		p.chooseFiles(a)
	}
}
func (p *filePane) outgoingReviewView(c *ui.Context, a fileActions, t fileText) {
	ui.Text(c, t("Review before sending", "送信前の確認")).Bold()
	fileDetail(c, t("Recipient: ", "送信先: ")+p.peer)
	for _, f := range p.review.Entries {
		fileDetail(c, fmt.Sprintf("%s · %d B", f.Path, f.Size))
	}
	fileDetail(c, fmt.Sprintf(t("Total: %d bytes", "合計: %d バイト"), p.review.TotalBytes))
	if a.peer != p.peer || a.process != p.process {
		fileDetail(c, t("Selection changed. Return to the reviewed recipient, or discard.", "選択が変わりました。確認した送信先に戻るか、破棄してください。"))
	}
	ui.Row(c).Gap(c.Theme().Space(1.5)).Children(func() {
		if ui.Button(c, t("Confirm send", "送信を確定")).Disabled(!p.canCommit(a)).Clicked() {
			p.commitFiles(a)
		}
		if ui.Button(c, t("Discard", "破棄")).Disabled(!p.canDiscard(a)).Clicked() {
			p.discardFiles(a)
		}
	})
}
func (p *filePane) incomingReviewView(c *ui.Context, a fileActions, t fileText) {
	offer := *p.incoming
	ui.Text(c, t("Review incoming offer", "受信オファーを確認")).Bold()
	fileDetail(c, t("Transfer: ", "転送: ")+offer.ID)
	fileDetail(c, t("From: ", "送信元: ")+offer.PeerID)
	for _, f := range offer.Entries {
		fileDetail(c, fmt.Sprintf("%s · %d B", f.Path, f.Size))
	}
	fileDetail(c, fmt.Sprintf(t("Total: %d bytes", "合計: %d バイト"), offer.TotalBytes))
	if !p.decline {
		p.destinationView(c, a, t)
	}
	label := t("Confirm accept", "受信を確定")
	if p.decline {
		label = t("Confirm decline", "辞退を確定")
	}
	if ui.Button(c, label).Disabled(!p.canConfirmIncoming(a)).Clicked() {
		p.confirmIncoming(a)
	}
	if ui.Button(c, t("Back", "戻る")).Disabled(p.busy).Clicked() {
		p.backIncoming()
	}
}
func (p *filePane) destinationView(c *ui.Context, a fileActions, t fileText) {
	fileDetail(c, t("Destination: ", "保存先: ")+p.destination)
	if ui.Button(c, t("Choose destination…", "保存先を選択…")).Disabled(!p.canChooseDestination(a)).Clicked() {
		p.chooseDestination(a)
	}
}
func (p *filePane) fileStatusView(c *ui.Context, a fileActions, t fileText) {
	if p.busy {
		fileDetail(c, t("Working…", "処理中…"))
	}
	if text := p.noticeText(a.demo, t); text != "" {
		fileDetail(c, text)
	}
	if p.uncertain && p.actionID != "" {
		fileDetail(c, t("Request: ", "リクエスト: ")+p.actionID)
	}
}
func (p *filePane) dispatchReceiptView(c *ui.Context, a fileActions, t fileText) {
	if len(p.receipt.Children) == 0 {
		return
	}
	ui.Text(c, t("Last dispatch recipients", "直近の送信先別状況")).Bold()
	for _, child := range p.receipt.Children {
		fileDetail(c, child.PeerID+" · "+child.TransferID+" · "+dispatchChildStatus(child, a, t))
	}
}
func (p *filePane) transferHistoryView(c *ui.Context, a fileActions, t fileText, ja bool) {
	transfers := visibleFileTransfers(a)
	if len(transfers) == 0 {
		fileDetail(c, t("No file transfers.", "ファイル転送はありません。"))
		return
	}
	for _, v := range transfers {
		p.transferRowView(c.Key(v.Direction+":"+v.ID), a, t, ja, v)
	}
}
func (p *filePane) transferRowView(c *ui.Context, a fileActions, t fileText, ja bool, v exchangeTransfer) {
	summary := fmt.Sprintf("%s · %s · %d/%d B", v.ID, exchangeStatus(v.Status, ja), v.CompletedBytes, v.TotalBytes)
	ui.Column(c).FillWidth().Gap(c.Theme().Space(1)).Children(func() {
		fileDetail(c, summary)
		for _, f := range v.Entries {
			fileDetail(c, fileLabel(f.Path))
		}
		if a.enabled && pendingOffer(v) && !p.acted[v.ID] {
			p.offerActionsView(c, a, t, v)
		}
	})
}
func (p *filePane) offerActionsView(c *ui.Context, a fileActions, t fileText, v exchangeTransfer) {
	ui.Row(c).Gap(c.Theme().Space(1.5)).Children(func() {
		if ui.Button(c, t("Review accept", "受信を確認")).Disabled(!p.canReviewIncoming(a, v)).Clicked() {
			p.reviewIncoming(a, v, false)
		}
		if ui.Button(c, t("Review decline", "辞退を確認")).Disabled(!p.canReviewIncoming(a, v)).Clicked() {
			p.reviewIncoming(a, v, true)
		}
	})
}
