package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/i18n"
	"go24/internal/sms"
)

// REST compatible with the portal, the SMS providers of applications (GO24DEV-1027): messageservice.sender.add,
// .delete, .list and messageservice.message.status.update by their names, for the token of an application; adding
// and deleting — of an administrator. The provider is one of «SMS» of the CRM: the cards, robots and
// business processes send through it, go24 posts each message to its handler.

func init() {
	for name, m := range map[string]portalMethod{
		"messageservice.sender.add":            {Scope: "crm:write", Run: (*Server).portalSMSSenderAdd},
		"messageservice.sender.delete":         {Scope: "crm:write", Run: (*Server).portalSMSSenderDelete},
		"messageservice.sender.list":           {Scope: "crm:read", Run: (*Server).portalSMSSenderList},
		"messageservice.message.status.update": {Scope: "crm:write", Run: (*Server).portalSMSStatus},
	} {
		portalMethods[name] = m
	}
}

// portalSMSErr is an error of the methods in the codes.
func (s *Server) portalSMSErr(c portalCall, err error) error {
	for e, code := range map[error]string{sms.ErrAppSenderExists: "ERROR_SENDER_ALREADY_INSTALLED", sms.ErrAppSenderMissing: "ERROR_SENDER_NOT_FOUND",
		sms.ErrAppSender: "ERROR_SENDER_VALIDATION_FAILURE", sms.ErrMessageMissing: "ERROR_MESSAGE_NOT_FOUND", sms.ErrMessageStatus: "ERROR_MESSAGE_STATUS_INCORRECT"} {
		if errors.Is(err, e) {
			var ie *i18n.Error
			errors.As(err, &ie)
			return s.portalErr(c.r, http.StatusBadRequest, code, ie.Key)
		}
	}
	return err
}

// portalSMSReady: the providers are there to add to.
func (s *Server) portalSMSReady(c portalCall) error {
	if s.SMS == nil {
		return s.portalErr(c.r, http.StatusNotFound, "ERROR_METHOD_NOT_FOUND", "err.portal_method", c.method)
	}
	return nil
}

// messageservice.sender.add: CODE, TYPE (SMS), HANDLER (on the application's host), NAME, DESCRIPTION → true.
func (s *Server) portalSMSSenderAdd(c portalCall) (portalOut, error) {
	if err := s.portalSMSReady(c); err != nil {
		return portalOut{}, err
	}
	a, err := s.portalAppOf(c, true)
	if err != nil {
		return portalOut{}, err
	}
	if t := portalStr(portalParam(c.in, "TYPE")); t != "" && !strings.EqualFold(t, "SMS") {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_SENDER_VALIDATION_FAILURE", "err.sms_app_sender")
	}
	x := sms.Sender{Code: portalStr(portalParam(c.in, "CODE")), Name: portalLang(portalParam(c.in, "NAME")), Login: strings.TrimSpace(portalStr(portalParam(c.in, "HANDLER"))),
		Description: portalLang(portalParam(c.in, "DESCRIPTION"))}
	if u, err := url.Parse(x.Login); err != nil || !a.Owns(u) {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_HANDLER_URL_MATCH", "err.app_handler_host")
	}
	if _, err := s.SMS.SaveAppSender(c.r.Context(), a.ID, x); err != nil {
		return portalOut{}, s.portalSMSErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// messageservice.sender.delete: CODE → true.
func (s *Server) portalSMSSenderDelete(c portalCall) (portalOut, error) {
	if err := s.portalSMSReady(c); err != nil {
		return portalOut{}, err
	}
	a, err := s.portalAppOf(c, true)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.SMS.DeleteAppSender(c.r.Context(), a.ID, portalStr(portalParam(c.in, "CODE"))); err != nil {
		return portalOut{}, s.portalSMSErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// messageservice.sender.list: the codes of the application's providers.
func (s *Server) portalSMSSenderList(c portalCall) (portalOut, error) {
	if err := s.portalSMSReady(c); err != nil {
		return portalOut{}, err
	}
	a, err := s.portalAppOf(c, false)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.SMS.AppSenders(c.r.Context(), a.ID)
	codes := []string{}
	for _, x := range list {
		codes = append(codes, x.Code)
	}
	return portalOut{Result: codes}, err
}

// messageservice.message.status.update: CODE, MESSAGE_ID (message_id the handler got), STATUS (queued, sent,
// delivered, undelivered, failed) → true.
func (s *Server) portalSMSStatus(c portalCall) (portalOut, error) {
	if err := s.portalSMSReady(c); err != nil {
		return portalOut{}, err
	}
	a, err := s.portalAppOf(c, false)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.SMS.SetAppStatus(c.r.Context(), a.ID, portalStr(portalParam(c.in, "CODE")), portalStr(portalParam(c.in, "MESSAGE_ID")),
		portalStr(portalParam(c.in, "STATUS"))); err != nil {
		return portalOut{}, s.portalSMSErr(c, err)
	}
	return portalOut{Result: true}, nil
}

// SMSAppCall gives a message to an application's provider (sms.Store.App).
func (s *Server) SMSAppCall(ctx context.Context, x sms.Sender, userID string, data map[string]any) error {
	return s.Apps.CallSMS(ctx, x.AppID, x.Login, userID, s.BaseURL, data)
}
