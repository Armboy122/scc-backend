package persistence

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// InitDB opens a GORM connection and optionally runs AutoMigrate/seed data.
func InitDB(dsn string, seedData bool, autoMigrate bool) (*gorm.DB, error) {
	gormLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	if autoMigrate {
		if err := migrate(db); err != nil {
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}

	if seedData {
		if err := seed(db); err != nil {
			return nil, fmt.Errorf("seed: %w", err)
		}
	}

	return db, nil
}

func migrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&WorkHubModel{},
		&OfficeModel{},
		&UserModel{},
		&RefreshTokenModel{},
		&CoverModel{},
		&WorkOrderModel{},
		&InstallationModel{},
		&BorrowModel{},
		&BorrowCoverModel{},
		&NotificationModel{},
	)
}

func seed(db *gorm.DB) error {
	var count int64
	db.Model(&WorkHubModel{}).Count(&count)
	if count > 0 {
		return nil // already seeded
	}

	log.Println("[seed] starting seed data...")

	// Initial master data mirrors the legacy work center / department reference data.
	// Runtime source of truth is the SCC PostgreSQL database on the VPS.
	workCenters := []struct {
		ID   int
		Name string
	}{
		{1, "กบษ."},
		{3, "พัทลุง"},
		{4, "สตูล"},
		{5, "สงขลา"},
		{6, "ระโนด"},
		{7, "หาดใหญ่"},
		{8, "จะนะ"},
		{9, "สะเดา"},
		{10, "ยะลา"},
		{11, "ปัตตานี"},
		{12, "สายบุรี"},
		{13, "เบตง"},
		{14, "นราธิวาส"},
		{15, "สุไหงโก-ลก"},
	}
	hubs := make([]*WorkHubModel, 0, len(workCenters))
	for _, wc := range workCenters {
		hubs = append(hubs, &WorkHubModel{ID: fmt.Sprintf("workcenter-%d", wc.ID), Name: wc.Name})
	}
	if err := db.Create(&hubs).Error; err != nil {
		return fmt.Errorf("seed hubs: %w", err)
	}

	departments := []struct {
		ID           int
		Name         string
		WorkCenterID int
	}{
		{1, "กบษ.", 1},
		{3, "กฟส.สะบ้าย้อย", 8},
		{4, "กฟส.นาทวี", 8},
		{5, "กฟส.เทพา", 8},
		{6, "กฟส.จะนะ", 8},
		{7, "กฟส.รือเสาะ", 14},
		{8, "กฟส.ระแงะ", 14},
		{9, "กฟส.ยี่งอ", 14},
		{10, "กฟส.จะแนะ", 14},
		{11, "กฟส.บาเจาะ", 14},
		{12, "กฟส.ตากใบ", 14},
		{13, "กฟส.เจาะไอร้อง", 14},
		{14, "กฟส.ศรีสาคร", 14},
		{15, "กฟจ.นราธิวาส", 14},
		{17, "กฟส.สุไหงโก-ลก", 15},
		{18, "กฟส.สุไหงปาดี", 15},
		{19, "กฟส.แว้ง", 15},
		{20, "กฟส.สุคิริน", 15},
		{21, "กฟจ.ยะลา", 10},
		{22, "กฟส.ยะหา", 10},
		{23, "กฟส.กาบัง", 10},
		{24, "กฟส.กรงปีนัง", 10},
		{25, "กฟส.บันนังสตา", 10},
		{26, "กฟส.ธารโต", 10},
		{27, "กฟส.รามัน", 10},
		{28, "เบตง", 13},
		{29, "กฟจ.พัทลุง", 3},
		{30, "กฟส.เขาชัยสน", 3},
		{31, "กฟส.กงหรา", 3},
		{32, "กฟส.ตะโหมด", 3},
		{33, "กฟส.บางแก้ว", 3},
		{34, "กฟส.ป่าบอน", 3},
		{35, "กฟส.ควนขนุน", 3},
		{36, "กฟส.ศรีบรรพต", 3},
		{37, "กฟส.ป่าพะยอม", 3},
		{38, "กฟส.ปากพะยูน", 3},
		{39, "กฟจ.สตูล", 4},
		{40, "กฟส.ท่าแพ", 4},
		{41, "กฟส.ละงู", 4},
		{42, "กฟส.ทุ่งหว้า", 4},
		{43, "กฟส.ควนกาหลง", 4},
		{44, "กฟส.มะนัง", 4},
		{45, "กฟจ.ปัตตานี", 11},
		{46, "กฟส.ยะหริ่ง", 11},
		{47, "กฟส.ยะรัง", 11},
		{48, "กฟส.โคกโพธิ์", 11},
		{49, "กฟส.แม่ลาน", 11},
		{50, "กฟส.หนองจิก", 11},
		{51, "กฟส.สายบุรี", 12},
		{52, "กฟส.ทุ่งยางแดง", 12},
		{53, "กฟส.ไม้แก่น", 12},
		{54, "กฟส.กะพ้อ", 12},
		{55, "กฟส.มายอ", 12},
		{56, "กฟส.ปะนาเระ", 12},
		{57, "กฟจ.สงขลา", 5},
		{58, "กฟส.สิงหนคร", 5},
		{59, "กฟส.ระโนด", 6},
		{60, "กฟส.กระแสสินธุ์", 6},
		{61, "กฟส.สทิงพระ", 6},
		{62, "กฟส.หาดใหญ่", 7},
		{63, "กฟส.บางกล่ำ", 7},
		{64, "กฟส.รัตภูมิ", 7},
		{65, "กฟส.ควนเนียง", 7},
		{66, "กฟส.นาหม่อม", 7},
		{67, "กฟส.สะเดา", 9},
		{68, "กฟส.ด่านนอก", 9},
		{69, "กฟส.ปาดังเบซาร์", 9},
		{70, "กฟส.พังลา", 9},
		{71, "กฟส.คลองหอยโข่ง", 9},
	}
	offices := make([]*OfficeModel, 0, len(departments))
	for _, dept := range departments {
		offices = append(offices, &OfficeModel{
			ID:        fmt.Sprintf("office-%d", dept.ID),
			Name:      dept.Name,
			WorkHubID: fmt.Sprintf("workcenter-%d", dept.WorkCenterID),
		})
	}
	if err := db.Create(&offices).Error; err != nil {
		return fmt.Errorf("seed offices: %w", err)
	}

	// Admin user (no office required)
	adminHash, _ := bcrypt.GenerateFromPassword([]byte("Admin1234!"), 12)
	adminUser := &UserModel{
		ID:           uuid.NewString(),
		Name:         "Administrator",
		Username:     "admin",
		PasswordHash: string(adminHash),
		Role:         "admin",
		IsActive:     true,
	}
	if err := db.Create(adminUser).Error; err != nil {
		return fmt.Errorf("seed admin: %w", err)
	}

	// Mock users for every office: one exec and one tech each.
	execHash, _ := bcrypt.GenerateFromPassword([]byte("Exec1234!"), 12)
	techHash, _ := bcrypt.GenerateFromPassword([]byte("Tech1234!"), 12)
	for _, office := range offices {
		officeID := office.ID
		execUser := &UserModel{
			ID:           uuid.NewString(),
			Name:         fmt.Sprintf("ผู้บริหาร %s", office.Name),
			Username:     fmt.Sprintf("exec-%s", office.ID),
			PasswordHash: string(execHash),
			Role:         "exec",
			OfficeID:     &officeID,
			IsActive:     true,
		}
		if err := db.Create(execUser).Error; err != nil {
			return fmt.Errorf("seed exec %s: %w", office.ID, err)
		}

		techUser := &UserModel{
			ID:           uuid.NewString(),
			Name:         fmt.Sprintf("ช่าง %s", office.Name),
			Username:     fmt.Sprintf("tech-%s", office.ID),
			PasswordHash: string(techHash),
			Role:         "tech",
			OfficeID:     &officeID,
			IsActive:     true,
		}
		if err := db.Create(techUser).Error; err != nil {
			return fmt.Errorf("seed tech %s: %w", office.ID, err)
		}
	}

	// 25 mock covers total. Asset codes are placeholders and can be edited later.
	const mockCoverCount = 25
	covers := make([]*CoverModel, 0, mockCoverCount)
	for i := 0; i < mockCoverCount; i++ {
		dept := departments[i%len(departments)]
		officeID := fmt.Sprintf("office-%d", dept.ID)
		assetCode := fmt.Sprintf("MOCK-ASSET-%03d", i+1)
		qrCode := fmt.Sprintf("QR-%03d", i+1)
		covers = append(covers, &CoverModel{
			ID:              uuid.NewString(),
			AssetCode:       assetCode,
			QRCode:          qrCode,
			Status:          "IN_STOCK",
			OwnerOfficeID:   officeID,
			CurrentOfficeID: officeID,
		})
	}
	if err := db.Create(&covers).Error; err != nil {
		return fmt.Errorf("seed covers: %w", err)
	}

	log.Println("[seed] seed data complete")
	return nil
}
