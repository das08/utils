package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/das08/utils/pkg/capture"
	"github.com/das08/utils/pkg/game"
	"github.com/das08/utils/pkg/settings"
	"github.com/georgysavva/scany/pgxscan"
	"github.com/nicksnyder/go-i18n/v2/i18n"
)

var DiscussCode = fmt.Sprintf("%d", game.DISCUSS)
var TasksCode = fmt.Sprintf("%d", game.TASKS)

type SimpleEventType int

const (
	Tasks SimpleEventType = iota
	Discuss
	PlayerDeath
	PlayerDisconnect
)

type SimpleEvent struct {
	EventType       SimpleEventType
	EventTimeOffset time.Duration
	Data            string
}

type GameStatistics struct {
	GameDuration time.Duration
	WinType      game.GameResult

	NumMeetings    int
	NumDeaths      int
	NumVotedOff    int
	NumDisconnects int
	Events         []SimpleEvent
}

func (stats *GameStatistics) ToString() string {
	buf := bytes.NewBuffer([]byte{})
	buf.WriteString(stats.FormatDurationAndWin())

	for _, v := range stats.Events {
		switch {
		case v.EventType == Tasks:
			buf.WriteString(fmt.Sprintf("%s into the game, Tasks phase resumed", v.EventTimeOffset.String()))
		case v.EventType == Discuss:
			buf.WriteString(fmt.Sprintf("%s into the game, Discussion was called", v.EventTimeOffset.String()))
		case v.EventType == PlayerDeath:
			player := game.Player{}
			err := json.Unmarshal([]byte(v.Data), &player)
			if err != nil {
				log.Println(err)
			} else {
				buf.WriteString(fmt.Sprintf("%s into the game, %s died", v.EventTimeOffset.String(), player.Name))
			}
		}
		buf.WriteRune('\n')
	}

	return buf.String()
}

// TODO localize
func (stats *GameStatistics) FormatDurationAndWin() string {
	buf := bytes.NewBuffer([]byte{})
	winner := ""
	switch stats.WinType {
	case game.HumansByTask:
		winner = "Crewmates won by completing tasks"
	case game.HumansByVote:
		winner = "Crewmates won by voting off the last Imposter"
	case game.HumansDisconnect:
		winner = "Crewmates won because the last Imposter disconnected"
	case game.ImpostorDisconnect:
		winner = "Imposters won because the last Human disconnected"
	case game.ImpostorBySabotage:
		winner = "Imposters won by sabotage"
	case game.ImpostorByVote:
		winner = "Imposters won by voting off the last Human"
	case game.ImpostorByKill:
		winner = "Imposters won by killing the last Human"
	}
	buf.WriteString("This display is VERY UNFINISHED and will be refined as time goes on!\n\n")

	buf.WriteString(fmt.Sprintf("Game lasted %s and %s\n", stats.GameDuration.String(), winner))
	buf.WriteString(fmt.Sprintf("There were %d meetings, %d deaths, and of those deaths, %d were from being voted off\n",
		stats.NumMeetings, stats.NumDeaths, stats.NumVotedOff))
	buf.WriteString("Game Events:\n")
	return buf.String()
}

func (stats *GameStatistics) ToDiscordEmbed(combinedID string, sett *settings.GuildSettings) *discordgo.MessageEmbed {
	title := sett.LocalizeMessage(&i18n.Message{
		ID:    "responses.matchStatsEmbed.Title",
		Other: "Game `{{.MatchID}}`",
	}, map[string]interface{}{
		"MatchID": combinedID,
	})

	fields := make([]*discordgo.MessageEmbedField, 0)

	fieldsOnLine := 0
	// TODO collapse by meeting/tasks "blocks" of data
	// TODO localize
	for _, v := range stats.Events {
		switch {
		case v.EventType == Tasks:
			fields = append(fields, &discordgo.MessageEmbedField{
				Name:   v.EventTimeOffset.String(),
				Value:  "🔨 Task Phase Begins",
				Inline: true,
			})
			fieldsOnLine++
		case v.EventType == Discuss:
			fields = append(fields, &discordgo.MessageEmbedField{
				Name:   v.EventTimeOffset.String(),
				Value:  "💬 Discussion Begins",
				Inline: true,
			})
			fieldsOnLine++
		case v.EventType == PlayerDeath:
			player := game.Player{}
			err := json.Unmarshal([]byte(v.Data), &player)
			if err != nil {
				log.Println(err)
			} else {
				fields = append(fields, &discordgo.MessageEmbedField{
					Name:   v.EventTimeOffset.String(),
					Value:  fmt.Sprintf("☠️ \"%s\" Died", player.Name),
					Inline: false,
				})
			}
			fieldsOnLine = 0
		}
		if fieldsOnLine == 2 {
			fields = append(fields, &discordgo.MessageEmbedField{
				Name:   "\u200B",
				Value:  "\u200B",
				Inline: true,
			})
		}
	}

	msg := discordgo.MessageEmbed{
		URL:         "",
		Type:        "",
		Title:       title,
		Description: stats.FormatDurationAndWin(),
		Timestamp:   "",
		Color:       10181046, // PURPLE
		Footer:      nil,
		Image:       nil,
		Thumbnail:   nil,
		Video:       nil,
		Provider:    nil,
		Author:      nil,
		Fields:      fields,
	}
	return &msg
}

func StatsFromGameAndEvents(pgame *PostgresGame, events []*PostgresGameEvent) GameStatistics {
	stats := GameStatistics{
		GameDuration: 0,
		WinType:      game.Unknown,
		NumMeetings:  0,
		NumDeaths:    0,
		Events:       []SimpleEvent{},
	}

	if pgame != nil {
		stats.GameDuration = time.Second * time.Duration(pgame.EndTime-pgame.StartTime)
		stats.WinType = game.GameResult(pgame.WinType)
	}

	if len(events) < 2 {
		return stats
	}

	for _, v := range events {
		if v.EventType == int16(capture.State) {
			if v.Payload == DiscussCode {
				stats.NumMeetings++
				stats.Events = append(stats.Events, SimpleEvent{
					EventType:       Discuss,
					EventTimeOffset: time.Second * time.Duration(v.EventTime-pgame.StartTime),
					Data:            "",
				})
			} else if v.Payload == TasksCode {
				stats.Events = append(stats.Events, SimpleEvent{
					EventType:       Tasks,
					EventTimeOffset: time.Second * time.Duration(v.EventTime-pgame.StartTime),
					Data:            "",
				})
			}
		} else if v.EventType == int16(capture.Player) {
			player := game.Player{}
			err := json.Unmarshal([]byte(v.Payload), &player)
			if err != nil {
				log.Println(err)
			} else {
				switch {
				case player.Action == game.DIED:
					stats.NumDeaths++
					stats.Events = append(stats.Events, SimpleEvent{
						EventType:       PlayerDeath,
						EventTimeOffset: time.Second * time.Duration(v.EventTime-pgame.StartTime),
						Data:            v.Payload,
					})
				case player.Action == game.EXILED:
					stats.NumVotedOff++
				case player.Action == game.DISCONNECTED:
					stats.NumDisconnects++
				}
			}
		}
	}

	return stats
}

func (psqlInterface *PsqlInterface) NumGamesPlayedOnGuild(guildID string) int64 {
	gid, _ := strconv.ParseInt(guildID, 10, 64)
	var r int64
	err := pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) FROM games WHERE guild_id=$1 AND end_time != -1;", gid)
	if err != nil {
		return -1
	}
	return r
}

func (psqlInterface *PsqlInterface) NumGamesWonAsRoleOnServer(guildID string, role game.GameRole) int64 {
	gid, _ := strconv.ParseInt(guildID, 10, 64)
	var r int64
	var err error
	if role == game.CrewmateRole {
		err = pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) FROM games WHERE guild_id=$1 AND (win_type=0 OR win_type=1 OR win_type=6)", gid)
	} else {
		err = pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) FROM games WHERE guild_id=$1 AND (win_type=2 OR win_type=3 OR win_type=4 OR win_type=5)", gid)
	}
	if err != nil {
		log.Println(err)
		return -1
	}
	return r
}

func (psqlInterface *PsqlInterface) NumGamesPlayedByUser(userID string) int64 {
	var r int64
	err := pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) FROM users_games WHERE user_id=$1;", userID)
	if err != nil {
		return -1
	}
	return r
}

func (psqlInterface *PsqlInterface) NumGuildsPlayedInByUser(userID string) int64 {
	var r int64
	err := pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(DISTINCT guild_id) FROM users_games WHERE user_id=$1;", userID)
	if err != nil {
		return -1
	}
	return r
}

func (psqlInterface *PsqlInterface) NumGamesPlayedByUserOnServer(userID, guildID string) int64 {
	var r int64
	gid, _ := strconv.ParseInt(guildID, 10, 64)
	err := pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) FROM users_games WHERE user_id=$1 AND guild_id=$2", userID, gid)
	if err != nil {
		return -1
	}
	return r
}

func (psqlInterface *PsqlInterface) NumWinsAsRoleOnServer(userID, guildID string, role int16) int64 {
	var r int64
	err := pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) FROM users_games WHERE user_id=$1 AND guild_id=$2 AND player_role=$3 AND player_won=true;", userID, guildID, role)
	if err != nil {
		return -1
	}
	return r
}

func (psqlInterface *PsqlInterface) NumWinsAsRole(userID string, role int16) int64 {
	var r int64
	err := pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) FROM users_games WHERE user_id=$1 AND player_role=$2 AND player_won=true;", userID, role)
	if err != nil {
		return -1
	}
	return r
}

func (psqlInterface *PsqlInterface) NumGamesAsRoleOnServer(userID, guildID string, role int16) int64 {
	var r int64
	err := pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) FROM users_games WHERE user_id=$1 AND guild_id=$2 AND player_role=$3;", userID, guildID, role)
	if err != nil {
		return -1
	}
	return r
}

func (psqlInterface *PsqlInterface) NumGamesAsRole(userID string, role int16) int64 {
	var r int64
	err := pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) FROM users_games WHERE user_id=$1 AND player_role=$2;", userID, role)
	if err != nil {
		return -1
	}
	return r
}

func (psqlInterface *PsqlInterface) NumWinsOnServer(userID, guildID string) int64 {
	var r int64
	err := pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) FROM users_games WHERE user_id=$1 AND guild_id=$2 AND player_won=true;", userID, guildID)
	if err != nil {
		return -1
	}
	return r
}

func (psqlInterface *PsqlInterface) NumWins(userID string) int64 {
	var r int64
	err := pgxscan.Get(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) FROM users_games WHERE user_id=$1 AND player_won=true;", userID)
	if err != nil {
		return -1
	}
	return r
}

type Int16ModeCount struct {
	Count int64 `db:"count"`
	Mode  int16 `db:"mode"`
}
type Uint64ModeCount struct {
	Count int64  `db:"count"`
	Mode  uint64 `db:"mode"`
}

type StringModeCount struct {
	Count int64  `db:"count"`
	Mode  string `db:"mode"`
}

//func (psqlInterface *PsqlInterface) ColorRankingForPlayer(userID string) []*Int16ModeCount {
//	r := []*Int16ModeCount{}
//	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT count(*),mode() within GROUP (ORDER BY player_color) AS mode FROM users_games WHERE user_id=$1 GROUP BY player_color ORDER BY count desc;", userID)
//
//	if err != nil {
//		log.Println(err)
//	}
//	return r
//}
func (psqlInterface *PsqlInterface) ColorRankingForPlayerOnServer(userID, guildID string) []*Int16ModeCount {
	r := []*Int16ModeCount{}
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT count(*),mode() within GROUP (ORDER BY player_color) AS mode FROM users_games WHERE user_id=$1 AND guild_id=$2 GROUP BY player_color ORDER BY count desc;", userID, guildID)

	if err != nil {
		log.Println(err)
	}
	return r
}

//func (psqlInterface *PsqlInterface) NamesRankingForPlayer(userID string) []*StringModeCount {
//	r := []*StringModeCount{}
//	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT count(*),mode() within GROUP (ORDER BY player_name) AS mode FROM users_games WHERE user_id=$1 GROUP BY player_name ORDER BY count desc;", userID)
//
//	if err != nil {
//		log.Println(err)
//	}
//	return r
//}

func (psqlInterface *PsqlInterface) NamesRankingForPlayerOnServer(userID, guildID string) []*StringModeCount {
	var r []*StringModeCount
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT count(*),mode() within GROUP (ORDER BY player_name) AS mode FROM users_games WHERE user_id=$1 AND guild_id=$2 GROUP BY player_name ORDER BY count desc;", userID, guildID)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) TotalGamesRankingForServer(guildID uint64) []*Uint64ModeCount {
	var r []*Uint64ModeCount
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT count(*),mode() within GROUP (ORDER BY user_id) AS mode FROM users_games WHERE guild_id=$1 GROUP BY user_id ORDER BY count desc;", guildID)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) OtherPlayersRankingForPlayerOnServer(userID, guildID string) []*PostgresOtherPlayerRanking {
	var r []*PostgresOtherPlayerRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT distinct B.user_id,"+
		"count(*) over (partition by B.user_id),"+
		"(count(*) over (partition by B.user_id)::decimal / (SELECT count(*) from users_games where user_id=$1 AND guild_id=$2))*100 as percent "+
		"FROM users_games A INNER JOIN users_games B ON A.game_id = B.game_id AND A.user_id != B.user_id "+
		"WHERE A.user_id=$1 AND A.guild_id=$2 "+
		"ORDER BY percent desc", userID, guildID)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) TotalWinRankingForServerByRole(guildID uint64, role int16) []*PostgresPlayerRanking {
	var r []*PostgresPlayerRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT DISTINCT user_id,"+
		"COUNT(user_id) FILTER ( WHERE player_won = TRUE ) AS win, "+
		// "COUNT(user_id) FILTER ( WHERE player_won = FALSE ) AS loss," +
		"COUNT(*) AS total, "+
		"(COUNT(user_id) FILTER ( WHERE player_won = TRUE )::decimal / COUNT(*)) * 100 AS win_rate "+
		// "(COUNT(user_id) FILTER ( WHERE player_won = FALSE )::decimal / COUNT(*)) * 100 AS loss_rate" +
		"FROM users_games "+
		"WHERE guild_id = $1 AND player_role = $2 "+
		"GROUP BY user_id "+
		"ORDER BY win_rate DESC", guildID, role)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) TotalWinRankingForServer(guildID uint64) []*PostgresPlayerRanking {
	var r []*PostgresPlayerRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT DISTINCT user_id,"+
		"COUNT(user_id) FILTER ( WHERE player_won = TRUE ) AS win, "+
		// "COUNT(user_id) FILTER ( WHERE player_won = FALSE ) AS loss," +
		"COUNT(*) AS total, "+
		"(COUNT(user_id) FILTER ( WHERE player_won = TRUE )::decimal / COUNT(*)) * 100 AS win_rate "+
		// "(COUNT(user_id) FILTER ( WHERE player_won = FALSE )::decimal / COUNT(*)) * 100 AS loss_rate" +
		"FROM users_games "+
		"WHERE guild_id = $1 "+
		"GROUP BY user_id "+
		"ORDER BY win_rate DESC", guildID)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) DeleteAllGamesForServer(guildID string) error {
	_, err := psqlInterface.Pool.Exec(context.Background(), "DELETE FROM games WHERE guild_id=$1", guildID)
	return err
}

func (psqlInterface *PsqlInterface) DeleteAllGamesForUser(userID string) error {
	_, err := psqlInterface.Pool.Exec(context.Background(), "DELETE FROM users_games WHERE user_id=$1", userID)
	return err
}

func (psqlInterface *PsqlInterface) BestTeammateByRole(userID, guildID string, role int16, leaderboardMin int) []*PostgresBestTeammatePlayerRanking {
	var r []*PostgresBestTeammatePlayerRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT DISTINCT users_games.user_id, "+
		"uG.user_id as teammate_id,"+
		"COUNT(users_games.player_won) as total, "+
		"COUNT(users_games.player_won) FILTER ( WHERE users_games.player_won = TRUE ) as win, "+
		"(COUNT(users_games.user_id) FILTER ( WHERE users_games.player_won = TRUE )::decimal / COUNT(*)) * 100 AS win_rate "+
		"FROM users_games "+
		"INNER JOIN users_games uG ON users_games.game_id = uG.game_id AND users_games.user_id <> uG.user_id "+
		"WHERE users_games.guild_id = $1 AND users_games.player_role = $2 AND uG.player_role = $2 AND users_games.user_id = $3 "+
		"GROUP BY users_games.user_id, uG.user_id "+
		"HAVING COUNT(users_games.player_won) >= $4 "+
		"ORDER BY win_rate DESC, win DESC, total DESC", guildID, role, userID, leaderboardMin)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) WorstTeammateByRole(userID, guildID string, role int16, leaderboardMin int) []*PostgresWorstTeammatePlayerRanking {
	var r []*PostgresWorstTeammatePlayerRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT DISTINCT users_games.user_id, "+
		"uG.user_id as teammate_id,"+
		"COUNT(users_games.player_won) as total, "+
		"COUNT(users_games.player_won) FILTER ( WHERE users_games.player_won = FALSE ) as loose, "+
		"(COUNT(users_games.user_id) FILTER ( WHERE users_games.player_won = FALSE )::decimal / COUNT(*)) * 100 AS loose_rate "+
		"FROM users_games "+
		"INNER JOIN users_games uG ON users_games.game_id = uG.game_id AND users_games.user_id <> uG.user_id "+
		"WHERE users_games.guild_id = $1 AND users_games.player_role = $2 AND uG.player_role = $2 AND users_games.user_id = $3 "+
		"GROUP BY users_games.user_id, uG.user_id "+
		"HAVING COUNT(users_games.player_won) >= $4 "+
		"ORDER BY loose_rate DESC, loose DESC, total DESC", guildID, role, userID, leaderboardMin)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) BestTeammateForServerByRole(guildID string, role int16, leaderboardMin int) []*PostgresBestTeammatePlayerRanking {
	var r []*PostgresBestTeammatePlayerRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT DISTINCT "+
		"CASE WHEN users_games.user_id > uG.user_id THEN users_games.user_id ELSE uG.user_id END, "+
		"CASE WHEN users_games.user_id > uG.user_id THEN uG.user_id ELSE users_games.user_id END as teammate_id, "+
		"COUNT(users_games.player_won) as total, "+
		"COUNT(users_games.player_won) FILTER ( WHERE users_games.player_won = TRUE ) as win, "+
		"(COUNT(users_games.user_id) FILTER ( WHERE users_games.player_won = TRUE )::decimal / COUNT(*)) * 100 AS win_rate "+
		"FROM users_games "+
		"INNER JOIN users_games uG ON users_games.game_id = uG.game_id AND users_games.user_id <> uG.user_id "+
		"WHERE users_games.guild_id = $1 AND users_games.player_role = $2 and uG.player_role = $2"+
		"GROUP BY users_games.user_id, uG.user_id "+
		"HAVING COUNT(users_games.player_won) >= $3 "+
		"ORDER BY win_rate DESC, win DESC, total DESC", guildID, role, leaderboardMin)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) WorstTeammateForServerByRole(guildID string, role int16, leaderboardMin int) []*PostgresWorstTeammatePlayerRanking {
	var r []*PostgresWorstTeammatePlayerRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT DISTINCT "+
		"CASE WHEN users_games.user_id > uG.user_id THEN users_games.user_id ELSE uG.user_id END, "+
		"CASE WHEN users_games.user_id > uG.user_id THEN uG.user_id ELSE users_games.user_id END as teammate_id,"+
		"COUNT(users_games.player_won) as total, "+
		"COUNT(users_games.player_won) FILTER ( WHERE users_games.player_won = FALSE ) as loose, "+
		"(COUNT(users_games.user_id) FILTER ( WHERE users_games.player_won = FALSE )::decimal / COUNT(*)) * 100 AS loose_rate "+
		"FROM users_games "+
		"INNER JOIN users_games uG ON users_games.game_id = uG.game_id AND users_games.user_id <> uG.user_id "+
		"WHERE users_games.guild_id = $1 AND users_games.player_role = $2 AND uG.player_role = $2"+
		"GROUP BY users_games.user_id, uG.user_id "+
		"HAVING COUNT(users_games.player_won) >= $3 "+
		"ORDER BY loose_rate DESC, loose DESC, total DESC", guildID, role, leaderboardMin)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) UserWinByActionAndRole(userdID, guildID string, action string, role int16) []*PostgresUserActionRanking {
	var r []*PostgresUserActionRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT users_games.user_id, "+
		"COUNT(ge.user_id) FILTER ( WHERE payload ->> 'Action' = $1 ) as total_action, "+
		"total_user.total as total, "+
		"total_user.win_rate as win_rate "+
		"FROM users_games "+
		"LEFT JOIN (SELECT user_id, guild_id, player_role, "+
		"COUNT(users_games.player_won) as total, "+
		"(COUNT(users_games.user_id) FILTER ( WHERE users_games.player_won = TRUE )::decimal / COUNT(*)) * 100 AS win_rate "+
		"FROM users_games "+
		"GROUP BY user_id, player_role, guild_id "+
		") total_user on total_user.user_id = users_games.user_id and users_games.player_role = total_user.player_role and users_games.guild_id = total_user.guild_id "+
		"LEFT JOIN game_events ge ON users_games.game_id = ge.game_id AND ge.user_id = users_games.user_id "+
		"WHERE users_games.user_id = $2 AND users_games.guild_id = $3 "+
		"AND users_games.player_role = $4 "+
		"GROUP BY users_games.user_id, total, win_rate "+
		"ORDER BY win_rate DESC, total DESC;", action, userdID, guildID, role)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) UserFrequentFirstTarget(userID, guildID string, action string, leaderboardSize int) []*PostgresUserMostFrequentFirstTargetRanking {
	var r []*PostgresUserMostFrequentFirstTargetRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) AS total_death, "+
		"users_games.user_id, total, "+
		"COUNT(*)::decimal / total * 100 AS death_rate "+
		"FROM users_games "+
		"LEFT JOIN LATERAL (SELECT game_events.user_id "+
		"FROM game_events WHERE game_events.game_id = users_games.game_id AND payload ->> 'Action' = $1 "+
		"ORDER BY event_time FETCH FIRST 1 ROW ONLY ) AS ge ON TRUE "+
		"LEFT JOIN LATERAL (SELECT count(*) AS total "+
		"FROM users_games WHERE users_games.user_id = ge.user_id AND users_games.guild_id = $2 AND player_role = 0) AS TOTAL_GAME ON TRUE "+
		"WHERE users_games.guild_id = $2 AND users_games.user_id = ge.user_id AND users_games.user_id = $3"+
		"GROUP BY users_games.user_id, total  "+
		"ORDER BY total_death DESC "+
		"LIMIT $4;", action, guildID, userID, leaderboardSize)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) UserMostFrequentFirstTargetForServer(guildID string, action string, leaderboardSize int) []*PostgresUserMostFrequentFirstTargetRanking {
	var r []*PostgresUserMostFrequentFirstTargetRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT COUNT(*) AS total_death, "+
		"users_games.user_id, total, "+
		"COUNT(*)::decimal / total * 100 AS death_rate "+
		"FROM users_games "+
		"LEFT JOIN LATERAL (SELECT game_events.user_id "+
		"FROM game_events WHERE game_events.game_id = users_games.game_id AND payload ->> 'Action' = $1 "+
		"ORDER BY event_time FETCH FIRST 1 ROW ONLY ) AS ge ON TRUE "+
		"LEFT JOIN LATERAL (SELECT COUNT(*) AS total "+
		"FROM users_games WHERE users_games.user_id = ge.user_id AND users_games.guild_id = $2 AND player_role = 0) AS TOTAL_GAME ON TRUE "+
		"WHERE users_games.guild_id = $2 AND users_games.user_id = ge.user_id AND total > 3"+
		"GROUP BY users_games.user_id, total  "+
		"ORDER BY death_rate DESC, total_death DESC "+
		"LIMIT $3;", action, guildID, leaderboardSize)

	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) UserMostFrequentKilledBy(userID, guildID string) []*PostgresUserMostFrequentKilledByanking {
	var r []*PostgresUserMostFrequentKilledByanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT users_games.user_id, "+
		"usG.user_id as teammate_id, "+
		"COUNT(ge.user_id) FILTER ( WHERE payload ->> 'Action' = $1 ) as total_death, "+
		"COUNT(usG.user_id) as encounter, (COUNT(ge.user_id) FILTER ( WHERE payload ->> 'Action' = $1 ))::decimal/count(usG.player_name) * 100 as death_rate "+
		"FROM users_games "+
		"LEFT JOIN users_games usG on users_games.game_id = usG.game_id and usG.player_role = $2 "+
		"LEFT JOIN (SELECT user_id, guild_id, player_role, COUNT(users_games.player_won) as total "+
		"FROM users_games "+
		"GROUP BY user_id, player_role, guild_id) total_user on total_user.user_id = users_games.user_id and users_games.player_role = total_user.player_role and users_games.guild_id = total_user.guild_id "+
		"LEFT JOIN game_events ge ON users_games.game_id = ge.game_id AND ge.user_id = $3 "+
		"WHERE users_games.guild_id = $4 AND users_games.user_id = $3 AND users_games.player_role = $5 "+
		"GROUP BY users_games.user_id, usG.user_id, users_games.user_id, total "+
		"ORDER BY death_rate DESC, total_death DESC, encounter DESC;", strconv.Itoa(int(game.DIED)), strconv.Itoa(int(game.ImposterRole)), userID, guildID, strconv.Itoa(int(game.CrewmateRole)))
	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) UserMostFrequentKilledByServer(guildID string) []*PostgresUserMostFrequentKilledByanking {
	var r []*PostgresUserMostFrequentKilledByanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r, "SELECT users_games.user_id, "+
		"usG.user_id as teammate_id, "+
		"COUNT(ge.user_id) FILTER ( WHERE payload ->> 'Action' = $1 ) as total_death, "+
		"COUNT(usG.user_id) as encounter, (COUNT(ge.user_id) FILTER ( WHERE payload ->> 'Action' = $1 ))::decimal/count(usG.player_name) * 100 as death_rate "+
		"FROM users_games "+
		"INNER JOIN users_games usG on users_games.game_id = usG.game_id and usG.player_role = $2 "+
		"INNER JOIN (SELECT user_id, guild_id, player_role, COUNT(users_games.player_won) as total "+
		"FROM users_games "+
		"GROUP BY user_id, player_role, guild_id) total_user on total_user.user_id = users_games.user_id and users_games.player_role = total_user.player_role and users_games.guild_id = total_user.guild_id "+
		"INNER JOIN game_events ge ON users_games.game_id = ge.game_id AND ge.user_id = users_games.user_id "+
		"WHERE users_games.guild_id = $3 AND users_games.player_role = $4 "+
		"GROUP BY users_games.user_id, usG.user_id, users_games.user_id, total "+
		"ORDER BY death_rate DESC, total_death DESC, encounter DESC;", strconv.Itoa(int(game.DIED)), strconv.Itoa(int(game.ImposterRole)), guildID, strconv.Itoa(int(game.CrewmateRole)))
	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) WinRateRanking(guildID string) []*PostgresWinRateRanking {
	var r []*PostgresWinRateRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r,
		"SELECT t.user_id, t.played_games, t.won_games,"+
			"(CASE WHEN played_games=0 THEN 0.0 ELSE (won_games::float)/played_games END) AS win_rate,"+
			"t.played_crew_games, t.won_crew_games,"+
			"(CASE WHEN played_crew_games=0 THEN 0.0 ELSE (won_crew_games::float)/played_crew_games END) AS crew_win_rate,"+
			"t.played_imposter_games, t.won_imposter_games,"+
			"(CASE WHEN played_imposter_games=0 THEN 0.0 ELSE (won_imposter_games::float)/played_imposter_games END) AS imposter_win_rate "+
			"FROM ("+
			"SELECT user_id,count(ug) AS played_games,"+
			"SUM(CASE WHEN ug.player_won THEN 1 ELSE 0 END) AS won_games,"+
			"SUM(CASE WHEN ug.player_role=0 THEN 1 ELSE 0 END) AS played_crew_games,"+
			"SUM(CASE WHEN ug.player_role=0 AND ug.player_won THEN 1 ELSE 0 END) AS won_crew_games,"+
			"SUM(CASE WHEN ug.player_role=1 THEN 1 ELSE 0 END) AS played_imposter_games,"+
			"SUM(CASE WHEN ug.player_role=1 AND ug.player_won THEN 1 ELSE 0 END) AS won_imposter_games "+
			"FROM users_games AS ug "+
			"WHERE guild_id=$1 "+
			"GROUP BY user_id) AS t "+
			"ORDER BY win_rate DESC;", guildID)
	if err != nil {
		log.Println(err)
	}
	return r
}

func (psqlInterface *PsqlInterface) SessionWinRateRanking(guildID string, connectCode string) []*PostgresWinRateRanking {
	var r []*PostgresWinRateRanking
	err := pgxscan.Select(context.Background(), psqlInterface.Pool, &r,
		"SELECT t.user_id, t.played_games, t.won_games,"+
			"(CASE WHEN played_games=0 THEN 0.0 ELSE (won_games::float)/played_games END) AS win_rate,"+
			"t.played_crew_games, t.won_crew_games,"+
			"(CASE WHEN played_crew_games=0 THEN 0.0 ELSE (won_crew_games::float)/played_crew_games END) AS crew_win_rate,"+
			"t.played_imposter_games, t.won_imposter_games,"+
			"(CASE WHEN played_imposter_games=0 THEN 0.0 ELSE (won_imposter_games::float)/played_imposter_games END) AS imposter_win_rate "+
			"FROM ("+
			"SELECT user_id,count(ug) AS played_games,"+
			"SUM(CASE WHEN ug.player_won THEN 1 ELSE 0 END) AS won_games,"+
			"SUM(CASE WHEN ug.player_role=0 THEN 1 ELSE 0 END) AS played_crew_games,"+
			"SUM(CASE WHEN ug.player_role=0 AND ug.player_won THEN 1 ELSE 0 END) AS won_crew_games,"+
			"SUM(CASE WHEN ug.player_role=1 THEN 1 ELSE 0 END) AS played_imposter_games,"+
			"SUM(CASE WHEN ug.player_role=1 AND ug.player_won THEN 1 ELSE 0 END) AS won_imposter_games "+
			"FROM users_games AS ug "+
			"INNER JOIN games ON games.game_id = ug.game_id "+
			"WHERE ug.guild_id=$1 "+
			"AND games.connect_code=$2 "+
			"GROUP BY user_id) AS t "+
			"ORDER BY win_rate DESC;", guildID, connectCode)
	if err != nil {
		log.Println(err)
	}
	return r
}
