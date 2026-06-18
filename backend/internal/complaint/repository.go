package complaint

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	DB *pgxpool.Pool
}

func (r *Repository) CreateComplaintWithAssignmentTx(
	ctx context.Context,
	citizenID string,
	req CreateComplaintRequest,
) (string, error) {

	tx, err := r.DB.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)

	locationJSON, _ := json.Marshal(req.Location)

	// Parse ward to int to match DB schema (extracting digits only)
	var wardInt int
	re := regexp.MustCompile(`[^0-9]+`)
	cleanWard := re.ReplaceAllString(req.Ward, "")
	if cleanWard != "" {
		fmt.Sscanf(cleanWard, "%d", &wardInt)
	}

	var complaintID string

	// 1️⃣ Insert Complaint and get generated UUID
	err = tx.QueryRow(ctx,
		`INSERT INTO complaints
		(citizen_id, category, severity, latitude, longitude, street, area, ward, city, location_json, image_url)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`,
		citizenID,
		req.Category,
		req.Severity,
		req.Latitude,
		req.Longitude,
		req.Street,
		req.Area,
		wardInt,
		req.City,
		locationJSON,
		req.ImageURL,
	).Scan(&complaintID)

	if err != nil {
		return "", err
	}

	// 2️⃣ Ward + Leave Aware Officer Selection
	var officerID string
	err = tx.QueryRow(ctx,
		`SELECT op.user_id
		 FROM officer_profiles op
		 WHERE op.ward_from <= $1
		   AND op.ward_to >= $1
		   AND op.is_active = TRUE
		   AND NOT EXISTS (
		       SELECT 1 FROM officer_leaves ol
		       WHERE ol.officer_id = op.user_id
		         AND ol.status = 'APPROVED'
		         AND CURRENT_DATE BETWEEN ol.from_date AND ol.to_date
		   )
		 LIMIT 1`,
		wardInt,
	).Scan(&officerID)

	// If a real database error occurred, return it
	if err != nil && err.Error() != "no rows in result set" {
		return "", err
	}

	// If no officer available (pgx.ErrNoRows) → keep as RAISED
	if err != nil {
		commitErr := tx.Commit(ctx)
		return complaintID, commitErr
	}

	// 3️⃣ Insert Assignment
	_, err = tx.Exec(ctx,
		`INSERT INTO work_order_assignments
		 (complaint_id, officer_id, assigned_role, is_active, created_at)
		 VALUES ($1,$2,'FIELD_OFFICER',TRUE,NOW())`,
		complaintID,
		officerID,
	)
	if err != nil {
		return "", err
	}

	// 4️⃣ Update Status
	_, err = tx.Exec(ctx,
		`UPDATE complaints
		 SET status='ALLOCATED', updated_at=NOW()
		 WHERE id=$1`,
		complaintID,
	)
	if err != nil {
		return "", err
	}

	return complaintID, tx.Commit(ctx)
}
func (r *Repository) GetComplaintsByCitizen(ctx context.Context, citizenID string) ([]Complaint, error) {

	rows, err := r.DB.Query(ctx,
		`SELECT id, category, severity,
		        COALESCE(latitude, 0),
		        COALESCE(longitude, 0),
		        COALESCE(street, ''),
		        COALESCE(area, ''),
		        COALESCE(ward, 0)::text,
		        COALESCE(city, ''),
		        status,
		        created_at,
		        COALESCE(image_url, ''),
		        '' AS completion_photo_url,
		        COALESCE(location_json, '{}'::jsonb),
		        COALESCE(rating, 0),
		        COALESCE(feedback_text, '')
		 FROM complaints
		 WHERE citizen_id=$1
		 ORDER BY created_at DESC`,
		citizenID)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var complaints []Complaint
	for rows.Next() {
		var c Complaint
		var locationJSON []byte

		err := rows.Scan(
			&c.ID,
			&c.Category,
			&c.Severity,
			&c.Latitude,
			&c.Longitude,
			&c.Street,
			&c.Area,
			&c.Ward,
			&c.City,
			&c.Status,
			&c.CreatedAt,
			&c.ImageURL,
			&c.CompletionImageURL,
			&locationJSON,
			&c.Rating,
			&c.FeedbackText,
		)
		if err != nil {
			return nil, err
		}

		_ = json.Unmarshal(locationJSON, &c.Location)
		complaints = append(complaints, c)
	}

	return complaints, nil
}

func (r *Repository) UpdateFeedback(
	ctx context.Context,
	citizenID,
	complaintID string,
	rating int,
	feedback string,
) error {

	_, err := r.DB.Exec(ctx,
		`UPDATE complaints
		 SET rating = $1,
		     feedback_text = $2
		 WHERE id = $3
		   AND citizen_id = $4
		   AND status = 'COMPLETED'`,
		rating,
		feedback,
		complaintID,
		citizenID,
	)

	return err
}
