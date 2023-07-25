// Copyright (c) 2023 Proton AG
//
// This file is part of Proton Mail Bridge.
//
// Proton Mail Bridge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Proton Mail Bridge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with Proton Mail Bridge. If not, see <https://www.gnu.org/licenses/>.


#ifndef BRIDGE_GUI_BUG_REPORT_FLOW_H
#define BRIDGE_GUI_BUG_REPORT_FLOW_H

//****************************************************************************************************************************************************
/// \brief Bug Report Flow parser.
//****************************************************************************************************************************************************
class BugReportFlow {

public: // member functions.
    BugReportFlow(); ///< Default constructor.
    BugReportFlow(BugReportFlow const &) = delete; ///< Disabled copy-constructor.
    BugReportFlow(BugReportFlow &&) = delete; ///< Disabled assignment copy-constructor.
    ~BugReportFlow() = default; ///< Destructor.

    bool parse(const QString& filepath); ///< Initialize the Bug Report Flow.

    QVariantList questionSet(quint8 categoryId) const; ///< Retrieve the set of question for a given bug category.
    bool setAnswer(quint8 questionId, QString const &answer); ///< Feed an answer for a given question.
    QString collectAnswers(quint8 categoryId) const; ///< Collect answer for a given set of questions.
    QStringList categories() const; ///< Getter for the 'bugCategories' property.
    QVariantList questions() const; ///< Getter for the 'bugQuestions' property.

private: // member functions
    bool parseFile(); ///< Parse the bug report flow description file.
    QJsonObject getJsonRootObj(); ///< Extract the JSON root object.
    QJsonObject getJsonDataObj(const QJsonObject& root); ///< Extract the JSON data object.
    QString getJsonVersion(const QJsonObject& root); ///< Extract the JSON version of the file.
    QJsonObject migrateData(const QJsonObject& data, const QString& version); ///< Migrate data if needed/possible.

private: // data members
    QString filepath_; ///< The file path of the BugReportFlow description file.
    QStringList categories_; ///< The list of Bug Category parsed from the description file.
    QVariantList questions_; ///< The list of Questions parsed from the description file.
    QList<QVariantList> questionsSet_; ///< Sets of questions per bug category.
    QMap<quint8, QString> answers_; ///< Map of QuestionId/Answer for the bug form.
};


#endif // BRIDGE_GUI_BUG_REPORT_FLOW_H
