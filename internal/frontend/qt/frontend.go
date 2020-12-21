// Copyright (c) 2021 Proton Technologies AG
//
// This file is part of ProtonMail Bridge.
//
// ProtonMail Bridge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// ProtonMail Bridge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with ProtonMail Bridge.  If not, see <https://www.gnu.org/licenses/>.

// +build !nogui

// Package qt is the Qt User interface for Desktop bridge.
//
// The FrontendQt implements Frontend interface: `frontend.go`.
// The helper functions are in `helpers.go`.
// Notification specific is written in `notification.go`.
// The AccountsModel is container providing account info to QML ListView.
//
// Since we are using QML there is only one Qt loop in  `ui.go`.
package qt

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ProtonMail/go-autostart"
	"github.com/ProtonMail/proton-bridge/internal/bridge"
	"github.com/ProtonMail/proton-bridge/internal/config/settings"
	"github.com/ProtonMail/proton-bridge/internal/events"
	"github.com/ProtonMail/proton-bridge/internal/frontend/autoconfig"
	qtcommon "github.com/ProtonMail/proton-bridge/internal/frontend/qt-common"
	"github.com/ProtonMail/proton-bridge/internal/frontend/types"
	"github.com/ProtonMail/proton-bridge/internal/locations"
	"github.com/ProtonMail/proton-bridge/internal/updater"
	"github.com/ProtonMail/proton-bridge/pkg/listener"
	"github.com/ProtonMail/proton-bridge/pkg/pmapi"
	"github.com/ProtonMail/proton-bridge/pkg/ports"
	"github.com/ProtonMail/proton-bridge/pkg/useragent"
	"github.com/sirupsen/logrus"
	"github.com/skratchdot/open-golang/open"
	"github.com/therecipe/qt/core"
	"github.com/therecipe/qt/gui"
	"github.com/therecipe/qt/qml"
	"github.com/therecipe/qt/widgets"
)

var log = logrus.WithField("pkg", "frontend-qt")
var accountMutex = &sync.Mutex{}

// API between Bridge and Qt.
//
// With this interface it is possible to control Qt-Gui interface using pointers to
// Qt and QML objects. QML signals and slots are connected via methods of GoQMLInterface.
type FrontendQt struct {
	version           string
	buildVersion      string
	programName       string
	showWindowOnStart bool
	panicHandler      types.PanicHandler
	locations         *locations.Locations
	settings          *settings.Settings
	eventListener     listener.Listener
	updater           types.Updater
	bridge            types.Bridger
	noEncConfirmator  types.NoEncConfirmator

	App        *widgets.QApplication      // Main Application pointer.
	View       *qml.QQmlApplicationEngine // QML engine pointer.
	MainWin    *core.QObject              // Pointer to main window inside QML.
	Qml        *GoQMLInterface            // Object accessible from both Go and QML for methods and signals.
	Accounts   *AccountsModel             // Providing data for  accounts ListView.
	programVer string                     // Program version (shown in help).

	authClient pmapi.Client

	auth *pmapi.Auth

	autostart *autostart.App

	// expand userID when added
	userIDAdded string

	restarter types.Restarter

	// saving most up-to-date update info to install it manually
	updateInfo updater.VersionInfo
}

// New returns a new Qt frontend for the bridge.
func New(
	version,
	buildVersion,
	programName string,
	showWindowOnStart bool,
	panicHandler types.PanicHandler,
	locations *locations.Locations,
	settings *settings.Settings,
	eventListener listener.Listener,
	updater types.Updater,
	bridge types.Bridger,
	noEncConfirmator types.NoEncConfirmator,
	autostart *autostart.App,
	restarter types.Restarter,
) *FrontendQt {
	tmp := &FrontendQt{
		version:           version,
		buildVersion:      buildVersion,
		programName:       programName,
		showWindowOnStart: showWindowOnStart,
		panicHandler:      panicHandler,
		locations:         locations,
		settings:          settings,
		eventListener:     eventListener,
		updater:           updater,
		bridge:            bridge,
		noEncConfirmator:  noEncConfirmator,
		programVer:        "v" + version,
		autostart:         autostart,
		restarter:         restarter,
	}

	// Nicer string for OS.
	currentOS := core.QSysInfo_PrettyProductName()
	bridge.SetCurrentOS(currentOS)

	return tmp
}

// InstanceExistAlert is a global warning window indicating an instance already exists.
func (s *FrontendQt) InstanceExistAlert() {
	log.Warn("Instance already exists")
	qtcommon.QtSetupCoreAndControls(s.programName, s.programVer)
	s.App = widgets.NewQApplication(len(os.Args), os.Args)
	s.View = qml.NewQQmlApplicationEngine(s.App)
	s.View.AddImportPath("qrc:///")
	s.View.Load(core.NewQUrl3("qrc:/BridgeUI/InstanceExistsWindow.qml", 0))
	_ = gui.QGuiApplication_Exec()
}

// Loop function for Bridge interface.
//
// It runs QtExecute in main thread with no additional function.
func (s *FrontendQt) Loop() (err error) {
	go func() {
		defer s.panicHandler.HandlePanic()
		s.watchEvents()
	}()
	err = s.qtExecute(func(s *FrontendQt) error { return nil })
	return err
}

func (s *FrontendQt) NotifyManualUpdate(update updater.VersionInfo, canInstall bool) {
	s.Qml.SetUpdateVersion(update.Version.String())
	s.Qml.SetUpdateLandingPage(update.LandingPage)
	s.Qml.SetUpdateReleaseNotesLink(update.ReleaseNotesPage)
	s.Qml.SetUpdateCanInstall(canInstall)
	s.updateInfo = update
	s.Qml.NotifyManualUpdate()
}

func (s *FrontendQt) NotifySilentUpdateInstalled() {
	s.Qml.NotifySilentUpdateRestartNeeded()
}

func (s *FrontendQt) NotifySilentUpdateError(err error) {
	s.Qml.NotifySilentUpdateError()
}

func (s *FrontendQt) watchEvents() {
	errorCh := s.getEventChannel(events.ErrorEvent)
	credentialsErrorCh := s.getEventChannel(events.CredentialsErrorEvent)
	outgoingNoEncCh := s.getEventChannel(events.OutgoingNoEncEvent)
	noActiveKeyForRecipientCh := s.getEventChannel(events.NoActiveKeyForRecipientEvent)
	internetOffCh := s.getEventChannel(events.InternetOffEvent)
	internetOnCh := s.getEventChannel(events.InternetOnEvent)
	secondInstanceCh := s.getEventChannel(events.SecondInstanceEvent)
	restartBridgeCh := s.getEventChannel(events.RestartBridgeEvent)
	addressChangedCh := s.getEventChannel(events.AddressChangedEvent)
	addressChangedLogoutCh := s.getEventChannel(events.AddressChangedLogoutEvent)
	logoutCh := s.getEventChannel(events.LogoutEvent)
	updateApplicationCh := s.getEventChannel(events.UpgradeApplicationEvent)
	newUserCh := s.getEventChannel(events.UserRefreshEvent)
	certIssue := s.getEventChannel(events.TLSCertIssue)
	for {
		select {
		case errorDetails := <-errorCh:
			imapIssue := strings.Contains(errorDetails, "IMAP failed")
			smtpIssue := strings.Contains(errorDetails, "SMTP failed")
			s.Qml.NotifyPortIssue(imapIssue, smtpIssue)
		case <-credentialsErrorCh:
			s.Qml.NotifyHasNoKeychain()
		case idAndSubject := <-outgoingNoEncCh:
			idAndSubjectSlice := strings.SplitN(idAndSubject, ":", 2)
			messageID := idAndSubjectSlice[0]
			subject := idAndSubjectSlice[1]
			s.Qml.ShowOutgoingNoEncPopup(messageID, subject)
		case email := <-noActiveKeyForRecipientCh:
			s.Qml.ShowNoActiveKeyForRecipient(email)
		case <-internetOffCh:
			s.Qml.SetConnectionStatus(false)
		case <-internetOnCh:
			s.Qml.SetConnectionStatus(true)
		case <-secondInstanceCh:
			s.Qml.ShowWindow()
		case <-restartBridgeCh:
			s.restarter.SetToRestart()
			// watchEvents is started in parallel with the Qt app.
			// If the event comes too early, app might not be ready yet.
			if s.App != nil {
				s.App.Quit()
			}
		case address := <-addressChangedCh:
			s.Qml.NotifyAddressChanged(address)
		case address := <-addressChangedLogoutCh:
			s.Qml.NotifyAddressChangedLogout(address)
		case userID := <-logoutCh:
			user, err := s.bridge.GetUser(userID)
			if err != nil {
				return
			}
			s.Qml.NotifyLogout(user.Username())
		case <-updateApplicationCh:
			s.Qml.ProcessFinished()
			s.Qml.NotifyForceUpdate()
		case <-newUserCh:
			s.Qml.LoadAccounts()
		case <-certIssue:
			s.Qml.ShowCertIssue()
		}
	}
}

func (s *FrontendQt) getEventChannel(event string) <-chan string {
	ch := make(chan string)
	s.eventListener.Add(event, ch)
	return ch
}

// Loop function for tests.
//
// It runs QtExecute in new thread with function returning itself after setup.
// Therefore it is possible to run tests on background.
func (s *FrontendQt) Start() (err error) {
	uiready := make(chan *FrontendQt)
	go func() {
		err := s.qtExecute(func(self *FrontendQt) error {
			// NOTE: Trick to send back UI by channel to access functionality
			// inside application thread. Other only uninitialized `ui` is visible.
			uiready <- self
			return nil
		})
		if err != nil {
			log.Error(err)
		}
		uiready <- nil
	}()

	// Receive UI pointer and set all pointers.
	running := <-uiready
	s.App = running.App
	s.View = running.View
	s.MainWin = running.MainWin
	return nil
}

// InvMethod runs the function with name `method` defined in RootObject of the QML.
// Used for tests.
func (s *FrontendQt) InvMethod(method string) error {
	arg := core.NewQGenericArgument("", nil)
	PauseLong()
	isGoodMethod := core.QMetaObject_InvokeMethod4(s.MainWin, method, arg, arg, arg, arg, arg, arg, arg, arg, arg, arg)
	if isGoodMethod == false {
		return errors.New("Wrong method " + method)
	}
	return nil
}

// qtExecute is the main function for starting the Qt application.
//
// It is better to have just one Qt application per program (at least per same
// thread). This functions reads the main user interface defined in QML files.
// The files are appended to library by Qt-QRC.
func (s *FrontendQt) qtExecute(Procedure func(*FrontendQt) error) error {
	qtcommon.QtSetupCoreAndControls(s.programName, s.programVer)
	s.App = widgets.NewQApplication(len(os.Args), os.Args)
	if runtime.GOOS == "linux" { // Fix default font.
		s.App.SetFont(gui.NewQFont2(FcMatchSans(), 12, int(gui.QFont__Normal), false), "")
	}
	s.App.SetQuitOnLastWindowClosed(false) // Just to make sure it's not closed.

	s.View = qml.NewQQmlApplicationEngine(s.App)
	// Add Go-QML bridge.
	s.Qml = NewGoQMLInterface(nil)
	s.Qml.SetIsShownOnStart(s.showWindowOnStart)
	s.Qml.SetFrontend(s) // provides access
	s.View.RootContext().SetContextProperty("go", s.Qml)

	// Set first start flag.
	s.Qml.SetIsFirstStart(s.settings.GetBool(settings.FirstStartGUIKey))
	s.settings.SetBool(settings.FirstStartGUIKey, false)

	// Check if it is first start after update (fresh version).
	lastVersion := s.settings.Get(settings.LastVersionKey)
	s.Qml.SetIsFreshVersion(lastVersion != "" && s.version != lastVersion)
	s.settings.Set(settings.LastVersionKey, s.version)

	// Add AccountsModel.
	s.Accounts = NewAccountsModel(nil)
	s.View.RootContext().SetContextProperty("accountsModel", s.Accounts)
	// Import path and load QML files.
	s.View.AddImportPath("qrc:///")
	s.View.Load(core.NewQUrl3("qrc:/ui.qml", 0))

	// List of used packages.
	s.Qml.SetCredits(bridge.Credits)
	s.Qml.SetFullversion(s.buildVersion)

	// Autostart.
	if s.Qml.IsFirstStart() {
		if s.autostart.IsEnabled() {
			if err := s.autostart.Disable(); err != nil {
				log.Error("First disable ", err)
				s.autostartError(err)
			}
		}
		s.toggleAutoStart()
	}
	if s.autostart.IsEnabled() {
		s.Qml.SetIsAutoStart(true)
	} else {
		s.Qml.SetIsAutoStart(false)
	}

	if s.settings.GetBool(settings.AutoUpdateKey) {
		s.Qml.SetIsAutoUpdate(true)
	} else {
		s.Qml.SetIsAutoUpdate(false)
	}

	if s.settings.GetBool(settings.AllowProxyKey) {
		s.Qml.SetIsProxyAllowed(true)
	} else {
		s.Qml.SetIsProxyAllowed(false)
	}

	if updater.UpdateChannel(s.settings.Get(settings.UpdateChannelKey)) == updater.EarlyChannel {
		s.Qml.SetIsEarlyAccess(true)
	} else {
		s.Qml.SetIsEarlyAccess(false)
	}

	s.eventListener.RetryEmit(events.TLSCertIssue)
	s.eventListener.RetryEmit(events.ErrorEvent)

	// Set reporting of outgoing email without encryption.
	s.Qml.SetIsReportingOutgoingNoEnc(s.settings.GetBool(settings.ReportOutgoingNoEncKey))

	defaultIMAPPort, _ := strconv.Atoi(settings.DefaultIMAPPort)
	defaultSMTPPort, _ := strconv.Atoi(settings.DefaultSMTPPort)

	// IMAP/SMTP ports.
	s.Qml.SetIsDefaultPort(
		defaultIMAPPort == s.settings.GetInt(settings.IMAPPortKey) &&
			defaultSMTPPort == s.settings.GetInt(settings.SMTPPortKey),
	)

	// Check QML is loaded properly.
	if len(s.View.RootObjects()) == 0 {
		return errors.New("QML not loaded properly")
	}

	// Obtain main window (need for invoke method).
	s.MainWin = s.View.RootObjects()[0]
	SetupSystray(s)

	// Injected procedure for out-of-main-thread applications.
	if err := Procedure(s); err != nil {
		return err
	}

	// Loop
	if ret := gui.QGuiApplication_Exec(); ret != 0 {
		err := errors.New("Event loop ended with return value:" + string(ret))
		log.Warn("QGuiApplication_Exec: ", err)
		return err
	}
	HideSystray()
	return nil
}

func (s *FrontendQt) openLogs() {
	logsPath, err := s.locations.ProvideLogsPath()
	if err != nil {
		return
	}

	go open.Run(logsPath)
}

func (s *FrontendQt) checkForUpdates() {
	go func() {
		version, err := s.updater.Check()

		if err != nil {
			logrus.WithError(err).Error("An error occurred while checking updates manually")
			s.Qml.NotifyManualUpdateError()
			return
		}

		if !s.updater.IsUpdateApplicable(version) {
			logrus.Debug("No need to update")
			return
		}

		logrus.WithField("version", version.Version).Info("An update is available")

		if !s.updater.CanInstall(version) {
			logrus.Debug("A manual update is required")
			s.NotifyManualUpdate(version, false)
			return
		}

		s.NotifyManualUpdate(version, true)
	}()
}

func (s *FrontendQt) openLicenseFile() {
	go open.Run(s.locations.GetLicenseFilePath())
}

func (s *FrontendQt) getLocalVersionInfo() {
	// NOTE: Fix this.
}

func (s *FrontendQt) sendBug(description, client, address string) (isOK bool) {
	isOK = true
	var accname = "No account logged in"
	if s.Accounts.Count() > 0 {
		accname = s.Accounts.get(0).Account()
	}
	if err := s.bridge.ReportBug(
		core.QSysInfo_ProductType(),
		core.QSysInfo_PrettyProductName(),
		description,
		accname,
		address,
		client,
	); err != nil {
		log.Error("while sendBug: ", err)
		isOK = false
	}
	return
}

func (s *FrontendQt) getLastMailClient() string {
	return s.bridge.GetCurrentClient()
}

func (s *FrontendQt) configureAppleMail(iAccount, iAddress int) {
	acc := s.Accounts.get(iAccount)

	user, err := s.bridge.GetUser(acc.UserID())
	if err != nil {
		log.Warn("UserConfigFromKeychain failed: ", acc.Account(), err)
		s.SendNotification(TabAccount, s.Qml.GenericErrSeeLogs())
		return
	}

	imapPort := s.settings.GetInt(settings.IMAPPortKey)
	imapSSL := false
	smtpPort := s.settings.GetInt(settings.SMTPPortKey)
	smtpSSL := s.settings.GetBool(settings.SMTPSSLKey)

	// If configuring apple mail for Catalina or newer, users should use SSL.
	doRestart := false
	if !smtpSSL && useragent.IsCatalinaOrNewer() {
		smtpSSL = true
		s.settings.SetBool(settings.SMTPSSLKey, true)
		log.Warn("Detected Catalina or newer with bad SMTP SSL settings, now using SSL, bridge needs to restart")
		doRestart = true
	}

	for _, autoConf := range autoconfig.Available() {
		if err := autoConf.Configure(imapPort, smtpPort, imapSSL, smtpSSL, user, iAddress); err != nil {
			log.Warn("Autoconfig failed: ", autoConf.Name(), err)
			s.SendNotification(TabAccount, s.Qml.GenericErrSeeLogs())
			return
		}
	}

	if doRestart {
		time.Sleep(2 * time.Second)
		s.restarter.SetToRestart()
		s.App.Quit()
	}
	return
}

func (s *FrontendQt) toggleAutoStart() {
	defer s.Qml.ProcessFinished()
	var err error
	if s.autostart.IsEnabled() {
		err = s.autostart.Disable()
	} else {
		err = s.autostart.Enable()
	}
	if err != nil {
		log.Error("Enable autostart: ", err)
		s.autostartError(err)
	}
	if s.autostart.IsEnabled() {
		s.Qml.SetIsAutoStart(true)
	} else {
		s.Qml.SetIsAutoStart(false)
	}
}

func (s *FrontendQt) toggleAutoUpdate() {
	defer s.Qml.ProcessFinished()

	if s.settings.GetBool(settings.AutoUpdateKey) {
		s.settings.SetBool(settings.AutoUpdateKey, false)
		s.Qml.SetIsAutoUpdate(false)
	} else {
		s.settings.SetBool(settings.AutoUpdateKey, true)
		s.Qml.SetIsAutoUpdate(true)
	}
}

func (s *FrontendQt) toggleEarlyAccess() {
	defer s.Qml.ProcessFinished()

	if updater.UpdateChannel(s.settings.Get(settings.UpdateChannelKey)) == updater.EarlyChannel {
		s.settings.Set(settings.UpdateChannelKey, string(updater.StableChannel))
		s.Qml.SetIsEarlyAccess(false)
	} else {
		s.settings.Set(settings.UpdateChannelKey, string(updater.EarlyChannel))
		s.Qml.SetIsEarlyAccess(true)
	}
}

func (s *FrontendQt) toggleAllowProxy() {
	defer s.Qml.ProcessFinished()

	if s.settings.GetBool(settings.AllowProxyKey) {
		s.settings.SetBool(settings.AllowProxyKey, false)
		s.bridge.DisallowProxy()
		s.Qml.SetIsProxyAllowed(false)
	} else {
		s.settings.SetBool(settings.AllowProxyKey, true)
		s.bridge.AllowProxy()
		s.Qml.SetIsProxyAllowed(true)
	}
}

func (s *FrontendQt) getIMAPPort() string {
	return s.settings.Get(settings.IMAPPortKey)
}

func (s *FrontendQt) getSMTPPort() string {
	return s.settings.Get(settings.SMTPPortKey)
}

// Return 0 -- port is free to use for server.
// Return 1 -- port is occupied.
func (s *FrontendQt) isPortOpen(portStr string) int {
	portInt, err := strconv.Atoi(portStr)
	if err != nil {
		return 1
	}
	if !ports.IsPortFree(portInt) {
		return 1
	}
	return 0
}

func (s *FrontendQt) setPortsAndSecurity(imapPort, smtpPort string, useSTARTTLSforSMTP bool) {
	s.settings.Set(settings.IMAPPortKey, imapPort)
	s.settings.Set(settings.SMTPPortKey, smtpPort)
	s.settings.SetBool(settings.SMTPSSLKey, !useSTARTTLSforSMTP)
}

func (s *FrontendQt) isSMTPSTARTTLS() bool {
	return !s.settings.GetBool(settings.SMTPSSLKey)
}

func (s *FrontendQt) checkInternet() {
	s.Qml.SetConnectionStatus(s.bridge.CheckConnection() == nil)
}

func (s *FrontendQt) switchAddressModeUser(iAccount int) {
	defer s.Qml.ProcessFinished()
	userID := s.Accounts.get(iAccount).UserID()
	user, err := s.bridge.GetUser(userID)
	if err != nil {
		log.Error("Get user for switch address mode failed: ", err)
		s.SendNotification(TabAccount, s.Qml.GenericErrSeeLogs())
		return
	}
	if err := user.SwitchAddressMode(); err != nil {
		log.Error("Switch address mode failed: ", err)
		s.SendNotification(TabAccount, s.Qml.GenericErrSeeLogs())
		return
	}
	s.userIDAdded = userID
}

func (s *FrontendQt) autostartError(err error) {
	if strings.Contains(err.Error(), "permission denied") {
		s.Qml.FailedAutostartCode("permission")
	} else if strings.Contains(err.Error(), "error code: 0x") {
		errorCode := err.Error()
		errorCode = errorCode[len(errorCode)-8:]
		s.Qml.FailedAutostartCode(errorCode)
	} else {
		s.Qml.FailedAutostartCode("")
	}
}

func (s *FrontendQt) toggleIsReportingOutgoingNoEnc() {
	shouldReport := !s.Qml.IsReportingOutgoingNoEnc()
	s.settings.SetBool(settings.ReportOutgoingNoEncKey, shouldReport)
	s.Qml.SetIsReportingOutgoingNoEnc(shouldReport)
}

func (s *FrontendQt) shouldSendAnswer(messageID string, shouldSend bool) {
	s.noEncConfirmator.ConfirmNoEncryption(messageID, shouldSend)
}

func (s *FrontendQt) saveOutgoingNoEncPopupCoord(x, y float32) {
	//prefs.SetFloat(prefs.OutgoingNoEncPopupCoordX, x)
	//prefs.SetFloat(prefs.OutgoingNoEncPopupCoordY, y)
}

func (s *FrontendQt) startManualUpdate() {
	go func() {
		err := s.updater.InstallUpdate(s.updateInfo)

		if err != nil {
			logrus.WithError(err).Error("An error occurred while installing updates manually")
			s.Qml.NotifyManualUpdateError()
		}

		s.Qml.NotifyManualUpdateRestartNeeded()
	}()
}
